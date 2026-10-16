package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	keywordCatalogSchemaVersion       = 1
	defaultKeywordManifestMaxBytes    = int64(128 * 1024)
	defaultKeywordCatalogMaxBytes     = int64(4 * 1024 * 1024)
	defaultKeywordCatalogRefresh      = 10 * time.Minute
	defaultKeywordCatalogHTTPTimeout  = 15 * time.Second
	keywordCatalogLastGoodFilename    = "last-good.json"
	keywordCatalogMaxNormalizedBytes  = 2 * 1024 * 1024
	keywordCatalogMaxDescriptionRunes = 240
)

// KeywordCatalogRemoteClient is intentionally narrow so catalog downloads can
// share the configured proxy without coupling the service to an HTTP library.
type KeywordCatalogRemoteClient interface {
	Fetch(ctx context.Context, rawURL, etag string, maxBytes int64) (KeywordCatalogRemoteResponse, error)
}

type KeywordCatalogRemoteResponse struct {
	Body        []byte
	ETag        string
	NotModified bool
}

type keywordCatalogManifest struct {
	SchemaVersion  int                        `json:"schema_version"`
	CatalogVersion string                     `json:"catalog_version"`
	PublishedAt    string                     `json:"published_at"`
	Sequence       uint64                     `json:"sequence"`
	KeywordCatalog keywordCatalogManifestFile `json:"keyword_catalog"`
}

type keywordCatalogManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type keywordCatalogDocument struct {
	SchemaVersion  int                      `json:"schema_version"`
	CatalogVersion string                   `json:"catalog_version"`
	PublishedAt    string                   `json:"published_at"`
	Packs          []keywordCatalogRulePack `json:"packs"`
}

type keywordCatalogRulePack struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Action      string   `json:"action"`
	Enabled     bool     `json:"enabled"`
	Keywords    []string `json:"keywords"`
}

type keywordCatalogDiskEnvelope struct {
	Manifest []byte `json:"manifest"`
	Catalog  []byte `json:"catalog"`
	ETag     string `json:"etag,omitempty"`
}

type keywordCatalogSnapshot struct {
	Packs          []ContentModerationKeywordRulePack
	CatalogVersion string
	Digest         string
	Sequence       uint64
	Source         string
	ETag           string
	UpdatedAt      time.Time
}

// KeywordCatalogStatus is safe to expose from a future admin endpoint. It
// intentionally contains no URL credentials or downloaded rule contents.
type KeywordCatalogStatus struct {
	Enabled        bool       `json:"enabled"`
	CatalogVersion string     `json:"catalog_version"`
	Sequence       uint64     `json:"sequence"`
	PackCount      int        `json:"pack_count"`
	KeywordCount   int        `json:"keyword_count"`
	Hash           string     `json:"hash"`
	Source         string     `json:"source"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	LastUpdatedAt  *time.Time `json:"last_updated_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
}

// KeywordCatalogService maintains one immutable catalog snapshot. Readers only
// perform an atomic load; downloads, validation, compilation inputs and disk IO
// stay entirely outside the gateway request path.
type KeywordCatalogService struct {
	cfg    config.KeywordCatalogConfig
	client KeywordCatalogRemoteClient

	snapshot atomic.Pointer[keywordCatalogSnapshot]
	refresh  sync.Mutex
	statusMu sync.RWMutex
	status   KeywordCatalogStatus
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewKeywordCatalogService(cfg *config.Config, client KeywordCatalogRemoteClient) *KeywordCatalogService {
	catalogCfg := config.KeywordCatalogConfig{}
	if cfg != nil {
		catalogCfg = cfg.Catalog.KeywordCatalog
	}
	embeddedPacks := defaultContentModerationKeywordRulePacks()
	embeddedDigest := digestKeywordRulePacks(embeddedPacks)
	svc := &KeywordCatalogService{
		cfg:    catalogCfg,
		client: client,
		status: KeywordCatalogStatus{
			Enabled:      catalogCfg.Enabled,
			Hash:         embeddedDigest,
			Source:       "embedded",
			PackCount:    len(embeddedPacks),
			KeywordCount: countContentModerationKeywords(embeddedPacks),
		},
		stopCh: make(chan struct{}),
	}
	svc.snapshot.Store(&keywordCatalogSnapshot{
		Packs:     cloneContentModerationKeywordRulePacks(embeddedPacks),
		Digest:    embeddedDigest,
		Source:    "embedded",
		UpdatedAt: time.Now(),
	})
	return svc
}

// Initialize loads a validated last-good snapshot synchronously and performs
// remote refreshes only in the background, so GitHub availability never gates
// process startup.
func (s *KeywordCatalogService) Initialize() {
	if s == nil {
		return
	}
	if err := s.loadLastGood(); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.setError(fmt.Errorf("load keyword catalog last-good: %w", err))
	}
	if !s.enabled() {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_, _ = s.ForceUpdate(context.Background())
		interval := time.Duration(s.cfg.RefreshIntervalMinutes) * time.Minute
		if interval < time.Minute {
			interval = defaultKeywordCatalogRefresh
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = s.ForceUpdate(context.Background())
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *KeywordCatalogService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *KeywordCatalogService) enabled() bool {
	return s != nil && s.cfg.Enabled && strings.TrimSpace(s.cfg.ManifestURL) != "" && s.client != nil
}

func (s *KeywordCatalogService) Status() KeywordCatalogStatus {
	if s == nil {
		return KeywordCatalogStatus{}
	}
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

// ForceUpdate validates the complete candidate before a single atomic store.
// Any error leaves both the current in-memory snapshot and last-good file intact.
func (s *KeywordCatalogService) ForceUpdate(ctx context.Context) (KeywordCatalogStatus, error) {
	if s == nil {
		return KeywordCatalogStatus{}, errors.New("keyword catalog service unavailable")
	}
	if !s.enabled() {
		return s.Status(), errors.New("keyword catalog remote sync is disabled")
	}
	s.refresh.Lock()
	defer s.refresh.Unlock()

	timeout := time.Duration(s.cfg.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultKeywordCatalogHTTPTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	manifestURL, err := validateKeywordCatalogManifestURL(s.cfg.ManifestURL, s.cfg.AllowInsecureHTTP)
	if err != nil {
		return s.failedUpdate(err)
	}
	current := s.snapshot.Load()
	etag := ""
	if current != nil {
		etag = current.ETag
	}
	manifestMax := s.cfg.MaxManifestBytes
	if manifestMax <= 0 || manifestMax > defaultKeywordManifestMaxBytes {
		manifestMax = defaultKeywordManifestMaxBytes
	}
	manifestResponse, err := s.client.Fetch(ctx, manifestURL, etag, manifestMax)
	if err != nil {
		return s.failedUpdate(fmt.Errorf("fetch manifest: %w", err))
	}
	s.markChecked("")
	if manifestResponse.NotModified {
		return s.Status(), nil
	}
	manifest, err := parseKeywordCatalogManifest(manifestResponse.Body)
	if err != nil {
		return s.failedUpdate(err)
	}
	if current != nil && current.Sequence > 0 {
		if manifest.Sequence < current.Sequence {
			return s.failedUpdate(fmt.Errorf("catalog sequence rollback: current=%d candidate=%d", current.Sequence, manifest.Sequence))
		}
	}
	catalogURL, err := resolveKeywordCatalogFileURL(manifestURL, manifest.KeywordCatalog.Path)
	if err != nil {
		return s.failedUpdate(err)
	}
	catalogMax := s.cfg.MaxCatalogBytes
	if catalogMax <= 0 || catalogMax > defaultKeywordCatalogMaxBytes {
		catalogMax = defaultKeywordCatalogMaxBytes
	}
	if manifest.KeywordCatalog.Size <= 0 || manifest.KeywordCatalog.Size > catalogMax {
		return s.failedUpdate(fmt.Errorf("keyword catalog declared size %d exceeds limit %d", manifest.KeywordCatalog.Size, catalogMax))
	}
	catalogResponse, err := s.client.Fetch(ctx, catalogURL, "", catalogMax)
	if err != nil {
		return s.failedUpdate(fmt.Errorf("fetch keyword catalog: %w", err))
	}
	if catalogResponse.NotModified {
		return s.failedUpdate(errors.New("keyword catalog resource unexpectedly returned not modified"))
	}
	if int64(len(catalogResponse.Body)) != manifest.KeywordCatalog.Size {
		return s.failedUpdate(fmt.Errorf("keyword catalog size mismatch: expected=%d actual=%d", manifest.KeywordCatalog.Size, len(catalogResponse.Body)))
	}
	digest := sha256.Sum256(catalogResponse.Body)
	digestHex := hex.EncodeToString(digest[:])
	if !strings.EqualFold(digestHex, manifest.KeywordCatalog.SHA256) {
		return s.failedUpdate(fmt.Errorf("keyword catalog sha256 mismatch: expected=%s actual=%s", manifest.KeywordCatalog.SHA256, digestHex))
	}
	document, packs, err := parseKeywordCatalogDocument(catalogResponse.Body)
	if err != nil {
		return s.failedUpdate(err)
	}
	if document.CatalogVersion != manifest.CatalogVersion {
		return s.failedUpdate(fmt.Errorf("catalog version mismatch: manifest=%q document=%q", manifest.CatalogVersion, document.CatalogVersion))
	}
	merged := overlayRemoteKeywordRulePacks(defaultContentModerationKeywordRulePacks(), packs)
	mergedDigest := digestKeywordRulePacks(merged)
	if current != nil && current.Sequence == manifest.Sequence && current.Sequence > 0 && current.Digest != mergedDigest {
		return s.failedUpdate(fmt.Errorf("catalog sequence %d was reused with different content", manifest.Sequence))
	}
	if err := s.saveLastGood(manifestResponse.Body, catalogResponse.Body, manifestResponse.ETag); err != nil {
		return s.failedUpdate(fmt.Errorf("persist keyword catalog last-good: %w", err))
	}
	now := time.Now()
	s.snapshot.Store(&keywordCatalogSnapshot{
		Packs:          cloneContentModerationKeywordRulePacks(merged),
		CatalogVersion: manifest.CatalogVersion,
		Digest:         mergedDigest,
		Sequence:       manifest.Sequence,
		Source:         "remote",
		ETag:           manifestResponse.ETag,
		UpdatedAt:      now,
	})
	s.statusMu.Lock()
	s.status.CatalogVersion = manifest.CatalogVersion
	s.status.Sequence = manifest.Sequence
	s.status.PackCount = len(merged)
	s.status.KeywordCount = countContentModerationKeywords(merged)
	s.status.Hash = mergedDigest
	s.status.Source = "remote"
	s.status.LastUpdatedAt = &now
	s.status.LastError = ""
	s.statusMu.Unlock()
	return s.Status(), nil
}

func (s *KeywordCatalogService) failedUpdate(err error) (KeywordCatalogStatus, error) {
	s.setError(err)
	return s.Status(), err
}

func (s *KeywordCatalogService) setError(err error) {
	if s == nil || err == nil {
		return
	}
	s.markChecked(err.Error())
}

func (s *KeywordCatalogService) markChecked(lastError string) {
	now := time.Now()
	s.statusMu.Lock()
	s.status.LastCheckedAt = &now
	s.status.LastError = lastError
	s.statusMu.Unlock()
}

func (s *KeywordCatalogService) currentSnapshot() *keywordCatalogSnapshot {
	if s == nil {
		return nil
	}
	return s.snapshot.Load()
}

func (s *KeywordCatalogService) loadLastGood() error {
	path := s.lastGoodPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(raw)) > defaultKeywordCatalogMaxBytes+defaultKeywordManifestMaxBytes+64*1024 {
		return errors.New("keyword catalog last-good envelope exceeds limit")
	}
	var envelope keywordCatalogDiskEnvelope
	if err := decodeStrictJSON(raw, &envelope); err != nil {
		return fmt.Errorf("decode last-good envelope: %w", err)
	}
	manifest, err := parseKeywordCatalogManifest(envelope.Manifest)
	if err != nil {
		return err
	}
	if int64(len(envelope.Catalog)) != manifest.KeywordCatalog.Size {
		return errors.New("last-good keyword catalog size mismatch")
	}
	digest := sha256.Sum256(envelope.Catalog)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), manifest.KeywordCatalog.SHA256) {
		return errors.New("last-good keyword catalog sha256 mismatch")
	}
	document, packs, err := parseKeywordCatalogDocument(envelope.Catalog)
	if err != nil {
		return err
	}
	if document.CatalogVersion != manifest.CatalogVersion {
		return errors.New("last-good catalog version mismatch")
	}
	merged := overlayRemoteKeywordRulePacks(defaultContentModerationKeywordRulePacks(), packs)
	mergedDigest := digestKeywordRulePacks(merged)
	info, _ := os.Stat(path)
	updatedAt := time.Now()
	if info != nil {
		updatedAt = info.ModTime()
	}
	s.snapshot.Store(&keywordCatalogSnapshot{
		Packs:          cloneContentModerationKeywordRulePacks(merged),
		CatalogVersion: manifest.CatalogVersion,
		Digest:         mergedDigest,
		Sequence:       manifest.Sequence,
		Source:         "disk",
		ETag:           envelope.ETag,
		UpdatedAt:      updatedAt,
	})
	s.statusMu.Lock()
	s.status.CatalogVersion = manifest.CatalogVersion
	s.status.Sequence = manifest.Sequence
	s.status.PackCount = len(merged)
	s.status.KeywordCount = countContentModerationKeywords(merged)
	s.status.Hash = mergedDigest
	s.status.Source = "disk"
	s.status.LastUpdatedAt = &updatedAt
	s.status.LastError = ""
	s.statusMu.Unlock()
	return nil
}

func (s *KeywordCatalogService) saveLastGood(manifest, catalog []byte, etag string) error {
	dir := strings.TrimSpace(s.cfg.DataDir)
	if dir == "" {
		dir = filepath.Join(".", "data", "keyword-catalog")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	envelope, err := json.Marshal(keywordCatalogDiskEnvelope{Manifest: manifest, Catalog: catalog, ETag: etag})
	if err != nil {
		return err
	}
	return writeKeywordCatalogFileAtomically(filepath.Join(dir, keywordCatalogLastGoodFilename), append(envelope, '\n'))
}

func (s *KeywordCatalogService) lastGoodPath() string {
	dir := strings.TrimSpace(s.cfg.DataDir)
	if dir == "" {
		dir = filepath.Join(".", "data", "keyword-catalog")
	}
	return filepath.Join(dir, keywordCatalogLastGoodFilename)
}

func parseKeywordCatalogManifest(raw []byte) (*keywordCatalogManifest, error) {
	if len(raw) == 0 || int64(len(raw)) > defaultKeywordManifestMaxBytes {
		return nil, errors.New("keyword catalog manifest is empty or too large")
	}
	var manifest keywordCatalogManifest
	if err := decodeStrictJSON(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode keyword catalog manifest: %w", err)
	}
	if manifest.SchemaVersion != keywordCatalogSchemaVersion {
		return nil, fmt.Errorf("unsupported keyword catalog manifest schema_version %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.CatalogVersion) == "" || len(manifest.CatalogVersion) > 80 {
		return nil, errors.New("invalid keyword catalog version")
	}
	if _, err := time.Parse(time.RFC3339, manifest.PublishedAt); err != nil {
		return nil, errors.New("invalid keyword catalog published_at")
	}
	if manifest.Sequence == 0 {
		return nil, errors.New("keyword catalog sequence must be positive")
	}
	if !validSHA256Hex(manifest.KeywordCatalog.SHA256) {
		return nil, errors.New("keyword catalog sha256 must be 64 hexadecimal characters")
	}
	if strings.TrimSpace(manifest.KeywordCatalog.Path) == "" {
		return nil, errors.New("keyword catalog path is required")
	}
	return &manifest, nil
}

func parseKeywordCatalogDocument(raw []byte) (*keywordCatalogDocument, []ContentModerationKeywordRulePack, error) {
	if len(raw) == 0 || int64(len(raw)) > defaultKeywordCatalogMaxBytes {
		return nil, nil, errors.New("keyword catalog document is empty or too large")
	}
	var document keywordCatalogDocument
	if err := decodeStrictJSON(raw, &document); err != nil {
		return nil, nil, fmt.Errorf("decode keyword catalog document: %w", err)
	}
	if document.SchemaVersion != keywordCatalogSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported keyword catalog schema_version %d", document.SchemaVersion)
	}
	if strings.TrimSpace(document.CatalogVersion) == "" || len(document.CatalogVersion) > 80 {
		return nil, nil, errors.New("invalid keyword catalog document version")
	}
	if _, err := time.Parse(time.RFC3339, document.PublishedAt); err != nil {
		return nil, nil, errors.New("invalid keyword catalog document published_at")
	}
	if len(document.Packs) == 0 || len(document.Packs) > maxContentModerationKeywordRulePacks {
		return nil, nil, fmt.Errorf("keyword catalog pack count must be between 1 and %d", maxContentModerationKeywordRulePacks)
	}
	seenPacks := make(map[string]struct{}, len(document.Packs))
	totalKeywords := 0
	totalNormalizedBytes := 0
	packs := make([]ContentModerationKeywordRulePack, 0, len(document.Packs))
	for _, rawPack := range document.Packs {
		id := normalizeContentModerationRulePackID(rawPack.ID)
		if id == "" || id != rawPack.ID {
			return nil, nil, fmt.Errorf("invalid or non-normalized keyword catalog pack id %q", rawPack.ID)
		}
		if _, exists := seenPacks[id]; exists {
			return nil, nil, fmt.Errorf("duplicate keyword catalog pack id %q", id)
		}
		seenPacks[id] = struct{}{}
		action := normalizeContentModerationKeywordRuleAction(rawPack.Action)
		if action == "" {
			return nil, nil, fmt.Errorf("invalid action for keyword catalog pack %q", id)
		}
		name := strings.TrimSpace(rawPack.Name)
		if name == "" || utf8.RuneCountInString(name) > 80 {
			return nil, nil, fmt.Errorf("invalid name for keyword catalog pack %q", id)
		}
		description := strings.TrimSpace(rawPack.Description)
		if utf8.RuneCountInString(description) > keywordCatalogMaxDescriptionRunes {
			return nil, nil, fmt.Errorf("description too long for keyword catalog pack %q", id)
		}
		for _, keyword := range rawPack.Keywords {
			if utf8.RuneCountInString(strings.TrimSpace(keyword)) > maxContentModerationBlockedKeywordRunes {
				return nil, nil, fmt.Errorf("keyword too long in catalog pack %q", id)
			}
		}
		keywords := normalizeBlockedKeywords(rawPack.Keywords)
		if len(keywords) == 0 || len(keywords) != len(rawPack.Keywords) {
			return nil, nil, fmt.Errorf("keyword catalog pack %q contains empty, duplicate or excessive keywords", id)
		}
		for _, keyword := range keywords {
			totalNormalizedBytes += len(normalizeContentPolicyText(keyword))
		}
		totalKeywords += len(keywords)
		if totalKeywords > maxContentModerationBlockedKeywords || totalNormalizedBytes > keywordCatalogMaxNormalizedBytes {
			return nil, nil, errors.New("keyword catalog exceeds keyword or normalized byte limit")
		}
		packs = append(packs, ContentModerationKeywordRulePack{
			ID: id, Name: name, Description: description, Action: action,
			Enabled: rawPack.Enabled, Keywords: keywords, BuiltIn: true,
		})
	}
	return &document, packs, nil
}

// overlayRemoteKeywordRulePacks replaces only stable IDs present remotely.
// A curated first release may contain a subset, so missing embedded packs are
// deliberately retained instead of disappearing from protection.
func overlayRemoteKeywordRulePacks(embedded, remote []ContentModerationKeywordRulePack) []ContentModerationKeywordRulePack {
	result := cloneContentModerationKeywordRulePacks(embedded)
	index := make(map[string]int, len(result))
	for i := range result {
		index[result[i].ID] = i
	}
	for _, pack := range remote {
		pack.BuiltIn = true
		pack.Keywords = append([]string(nil), pack.Keywords...)
		if i, ok := index[pack.ID]; ok {
			result[i] = pack
			continue
		}
		if len(result) >= maxContentModerationKeywordRulePacks {
			break
		}
		index[pack.ID] = len(result)
		result = append(result, pack)
	}
	return result
}

// mergeKeywordCatalogWithLocalDeltas makes the public catalog authoritative
// for built-in content. Version 1 stores only explicit enabled/action changes;
// local custom packs remain full records. Legacy saved built-in copies are
// deliberately ignored because there is no reliable way to distinguish an
// administrator choice from defaults that old versions serialized wholesale.
func mergeKeywordCatalogWithLocalDeltas(
	base []ContentModerationKeywordRulePack,
	overrides []ContentModerationKeywordRulePackOverride,
	legacy []ContentModerationKeywordRulePack,
	version int,
) []ContentModerationKeywordRulePack {
	result := cloneContentModerationKeywordRulePacks(base)
	index := make(map[string]int, len(result))
	for i := range result {
		index[result[i].ID] = i
	}
	if version >= 1 {
		for _, override := range normalizeContentModerationKeywordRulePackOverrides(overrides) {
			if i, exists := index[override.ID]; exists {
				if override.Enabled != nil {
					result[i].Enabled = *override.Enabled
				}
				if override.Action != nil {
					result[i].Action = *override.Action
				}
			}
		}
	}
	for _, pack := range legacy {
		id := normalizeContentModerationRulePackID(pack.ID)
		if id == "" || pack.BuiltIn || len(result) >= maxContentModerationKeywordRulePacks {
			continue
		}
		if _, exists := index[id]; exists {
			continue
		}
		pack.ID = id
		pack.Action = normalizeContentModerationKeywordRuleAction(pack.Action)
		pack.Keywords = normalizeBlockedKeywords(pack.Keywords)
		if pack.Action == "" || len(pack.Keywords) == 0 {
			continue
		}
		pack.BuiltIn = false
		index[id] = len(result)
		result = append(result, pack)
	}
	return result
}

func validateKeywordCatalogManifestURL(raw string, allowInsecureHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid keyword catalog manifest URL")
	}
	if parsed.Scheme != "https" && !(allowInsecureHTTP && parsed.Scheme == "http") {
		return "", errors.New("keyword catalog manifest URL must use HTTPS")
	}
	return parsed.String(), nil
}

func resolveKeywordCatalogFileURL(manifestURL, relativePath string) (string, error) {
	base, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(strings.TrimSpace(relativePath))
	if err != nil || reference.IsAbs() || reference.Host != "" || reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" {
		return "", errors.New("keyword catalog path must be a same-origin relative path")
	}
	for _, part := range strings.Split(strings.ReplaceAll(reference.Path, "\\", "/"), "/") {
		if part == ".." {
			return "", errors.New("keyword catalog path cannot traverse parent directories")
		}
	}
	resolved := base.ResolveReference(reference)
	if !strings.EqualFold(resolved.Scheme, base.Scheme) || !strings.EqualFold(resolved.Host, base.Host) {
		return "", errors.New("keyword catalog resource must use manifest origin")
	}
	return resolved.String(), nil
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}

func digestKeywordRulePacks(packs []ContentModerationKeywordRulePack) string {
	raw, _ := json.Marshal(packs)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func countContentModerationKeywords(packs []ContentModerationKeywordRulePack) int {
	total := 0
	for _, pack := range packs {
		total += len(pack.Keywords)
	}
	return total
}

func writeKeywordCatalogFileAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keyword-catalog-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceFileAtomically(tmpPath, path)
}
