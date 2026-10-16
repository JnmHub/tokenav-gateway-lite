package gatewaylite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

const bindingSchema = `CREATE TABLE IF NOT EXISTS gateway_lite_binding (
 id INTEGER PRIMARY KEY CHECK (id=1), revision BIGINT NOT NULL,
 payload TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`

// Destination is frozen for a runtime generation. Credentials are only marshalled
// into the private database record, never into the administrator status response.
type BindingDestination struct {
	BindingID    string `json:"binding_id"`
	URL          string `json:"url"`
	Credential   string `json:"credential"`
	GatewayCode  string `json:"gateway_code"`
	NodeScope    string `json:"node_scope"`
	RedisPrefix  string `json:"redis_prefix"`
	AdminSyncKey string `json:"admin_sync_key"`
	Name         string `json:"name"`
	PublicURL    string `json:"public_url"`
	Region       string `json:"region"`
	NodeID       int64  `json:"node_id"`
	Legacy       bool   `json:"legacy,omitempty"`
}

type BindingRecord struct {
	Revision            int64               `json:"revision"`
	InstanceID          string              `json:"instance_id"`
	Phase               string              `json:"phase"`
	Current             *BindingDestination `json:"current,omitempty"`
	Pending             *BindingDestination `json:"pending,omitempty"`
	LastError           string              `json:"last_error,omitempty"`
	HandshakeAt         int64               `json:"handshake_at,omitempty"`
	SnapshotAt          int64               `json:"snapshot_at,omitempty"`
	MediaUnknown        bool                `json:"media_unknown,omitempty"`
	DisconnectOperation string              `json:"disconnect_operation,omitempty"`
	RemoteDetached      bool                `json:"remote_detached,omitempty"`
}

type BindingStore interface {
	Load(context.Context) (BindingRecord, error)
	Save(context.Context, BindingRecord, int64) error
}

var ErrBindingConflict = errors.New("binding_state_conflict")

type SQLBindingStore struct{ DB *sql.DB }

func (s SQLBindingStore) Load(ctx context.Context) (BindingRecord, error) {
	var r BindingRecord
	var payload string
	if s.DB == nil {
		return r, errors.New("binding_database_unavailable")
	}
	if _, err := s.DB.ExecContext(ctx, bindingSchema); err != nil {
		return r, err
	}
	if _, err := s.DB.ExecContext(ctx, bindingMediaSchema); err != nil {
		return r, err
	}
	err := s.DB.QueryRowContext(ctx, `SELECT payload FROM gateway_lite_binding WHERE id=1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(payload), &r)
	if err == nil && (r.InstanceID == "" || !validBindingPhase(r.Phase)) {
		err = errors.New("binding_record_invalid")
	}
	return r, err
}
func (s SQLBindingStore) Save(ctx context.Context, r BindingRecord, expected int64) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var result sql.Result
	if expected == 0 {
		result, err = s.DB.ExecContext(ctx, `INSERT INTO gateway_lite_binding(id,revision,payload) VALUES(1,$1,$2) ON CONFLICT(id) DO NOTHING`, r.Revision, string(payload))
	} else {
		result, err = s.DB.ExecContext(ctx, `UPDATE gateway_lite_binding SET revision=$1,payload=$2,updated_at=NOW() WHERE id=1 AND revision=$3`, r.Revision, string(payload), expected)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrBindingConflict
	}
	return err
}
func validBindingPhase(p string) bool {
	switch p {
	case "unbound", "registering", "activating", "bound", "draining", "blocked":
		return true
	}
	return false
}

type BindingManager struct {
	mu          sync.Mutex
	store       BindingStore
	record      BindingRecord
	inflight    int64
	remote      BindingRemote
	drain       func(context.Context, BindingRecord) (BindingDrain, error)
	restart     func() bool
	mediaDB     *sql.DB
	operationMu sync.Mutex
}

var defaultBindingManager atomic.Pointer[BindingManager]

func DefaultBindingManager() *BindingManager     { return defaultBindingManager.Load() }
func SetDefaultBindingManager(m *BindingManager) { defaultBindingManager.Store(m) }

func NewBindingManager(ctx context.Context, store BindingStore, legacy config.GatewayLiteConfig) (*BindingManager, error) {
	r, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	if r.Revision == 0 {
		r = BindingRecord{Revision: 1, InstanceID: uuid.NewString(), Phase: "unbound"}
		if legacy.ControlPlaneURL != "" {
			code := legacy.GatewayCode
			if code == "" {
				code = legacy.Region
			}
			if code == "" {
				code = "default"
			}
			r.Current = &BindingDestination{BindingID: uuid.NewString(), URL: legacy.ControlPlaneURL, Credential: legacy.ControlPlaneToken, GatewayCode: code, NodeScope: code, RedisPrefix: NormalizeRedisPrefix(legacy.RedisPrefix), AdminSyncKey: legacy.AdminSyncKey, Region: legacy.Region, Legacy: true}
			r.Phase = "activating"
		}
		if err = store.Save(ctx, r, 0); err != nil {
			return nil, err
		}
	}
	if r.Current != nil && r.Phase == "bound" {
		// Persisted identity survives restart, but this generation must confirm
		// its own handshake and applied snapshot before it reports bound again.
		next := cloneBindingRecord(r)
		next.Phase = "activating"
		next.HandshakeAt = 0
		next.SnapshotAt = 0
		next.Revision = r.Revision + 1
		if err = store.Save(ctx, next, r.Revision); err != nil {
			return nil, err
		}
		r = next
	}
	m := &BindingManager{store: store, record: r, remote: HTTPBindingRemote{}, restart: RequestBindingRestart}
	return m, nil
}
func (m *BindingManager) Record() BindingRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneBindingRecord(m.record)
}
func cloneBindingRecord(r BindingRecord) BindingRecord {
	if r.Current != nil {
		d := *r.Current
		r.Current = &d
	}
	if r.Pending != nil {
		d := *r.Pending
		r.Pending = &d
	}
	return r
}
func (m *BindingManager) saveLocked(ctx context.Context, next BindingRecord) error {
	next.Revision = m.record.Revision + 1
	if err := m.store.Save(ctx, next, m.record.Revision); err != nil {
		return err
	}
	m.record = next
	return nil
}

// RuntimeDestination never borrows an old URL or credential after local detach.
func (m *BindingManager) RuntimeDestination() *BindingDestination {
	r := m.Record()
	if r.Pending != nil && r.Phase == "activating" {
		return r.Pending
	}
	return r.Current
}
func (m *BindingManager) ApplyStartup(cfg *config.GatewayLiteConfig) {
	d := m.RuntimeDestination()
	if d == nil {
		r := m.Record()
		cfg.ControlPlaneURL = ""
		cfg.ControlPlaneToken = ""
		cfg.GatewayCode = "unbound-" + r.InstanceID
		cfg.RedisPrefix = "gl:unbound:" + r.InstanceID
		cfg.AdminSyncKey = ""
		return
	}
	cfg.ControlPlaneURL = d.URL
	cfg.ControlPlaneToken = d.Credential
	cfg.GatewayCode = d.GatewayCode
	cfg.RedisPrefix = d.RedisPrefix
	cfg.AdminSyncKey = d.AdminSyncKey
	cfg.Region = d.Region
	if d.PublicURL!="" {cfg.MediaProxyPublicURL=d.PublicURL}
}
func (m *BindingManager) IdentityAllowed(cfg config.GatewayLiteConfig) bool {
	d := m.RuntimeDestination()
	if d == nil {
		return cfg.ControlPlaneURL == "" && cfg.ControlPlaneToken == ""
	}
	return cfg.ControlPlaneURL == d.URL && cfg.ControlPlaneToken == d.Credential && cfg.GatewayCode == d.GatewayCode && NormalizeRedisPrefix(cfg.RedisPrefix) == d.RedisPrefix && cfg.Region == d.Region && cfg.AdminSyncKey == d.AdminSyncKey
}
func (m *BindingManager) EnterRequest(ctx context.Context, unknownMedia bool) (func(), bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	allowed := m.record.Phase == "bound" || (m.record.Phase == "activating" && m.record.Current != nil && m.record.Current.Legacy)
	if !allowed {
		return nil, false
	}
	if unknownMedia && !m.record.MediaUnknown {
		next := cloneBindingRecord(m.record)
		next.MediaUnknown = true
		// Seedance has no durable unsettled ledger. Persist uncertainty before dispatch;
		// Redis TTL expiration must never be interpreted as settlement evidence.
		if err := m.saveLocked(ctx, next); err != nil {
			return nil, false
		}
	}
	m.inflight++
	var once sync.Once
	return func() { once.Do(func() { m.mu.Lock(); m.inflight--; m.mu.Unlock() }) }, true
}
func (m *BindingManager) Activate(ctx context.Context, syncFull func(context.Context) error) error {
	d:=m.RuntimeDestination();if d==nil{return nil}
	return m.activateGeneration(ctx,d.BindingID,syncFull)
}
func (m *BindingManager) activateGeneration(ctx context.Context,generationID string,syncFull func(context.Context)error)error{
	r := m.Record()
	d:=r.Current;if r.Pending!=nil && r.Phase=="activating"{d=r.Pending}
	if d == nil || d.BindingID!=generationID {
		return nil
	}
	nodeID, handshakeErr := m.remote.Handshake(ctx, *d)
	if handshakeErr != nil {
		m.activationError(ctx, "主站握手失败，请重试连接。")
		return handshakeErr
	}
	if err := syncFull(ctx); err != nil {
		m.activationError(ctx, "配置快照未同步完成，请重试连接。")
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	activeID:="";if m.record.Pending!=nil{activeID=m.record.Pending.BindingID}else if m.record.Current!=nil{activeID=m.record.Current.BindingID}
	if activeID!=generationID || m.record.Phase != "activating" || (r.Pending != nil && (m.record.Pending == nil || m.record.Pending.BindingID != r.Pending.BindingID)) {
		return nil
	}
	next := cloneBindingRecord(m.record)
	d.NodeID = nodeID
	next.Current = d
	next.Pending = nil
	next.Phase = "bound"
	next.LastError = ""
	next.HandshakeAt = time.Now().UnixMilli()
	next.SnapshotAt = next.HandshakeAt
	return m.saveLocked(ctx, next)
}
func (m *BindingManager) activationError(ctx context.Context, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneBindingRecord(m.record)
	next.LastError = message
	_ = m.saveLocked(ctx, next)
}

var bindingRestartRequests = make(chan struct{}, 1)
var bindingRestartEnabled atomic.Bool

func EnableBindingRestart(enabled bool)       { bindingRestartEnabled.Store(enabled) }
func BindingRestartAvailable() bool           { return bindingRestartEnabled.Load() }
func BindingRestartRequests() <-chan struct{} { return bindingRestartRequests }
func RequestBindingRestart() bool {
	if !BindingRestartAvailable() {
		return false
	}
	select {
	case bindingRestartRequests <- struct{}{}:
	default:
	}
	return true
}
