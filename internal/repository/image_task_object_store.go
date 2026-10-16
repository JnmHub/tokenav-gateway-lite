package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type LocalImageTaskObjectStore struct {
	root       string
	signingKey []byte
	publicPath string
}

// DeletePrefixOlderThan removes stale objects under a controlled namespace.
// Synchronous media objects do not persist a database row, so the filesystem
// modification time is the authoritative last-used timestamp; Put replaces
// deterministic keys atomically and refreshes that timestamp. Sidecar meta
// files are removed together with their payload.
func (s *LocalImageTaskObjectStore) DeletePrefixOlderThan(_ context.Context, prefix string, before time.Time, limit int) (int, error) {
	if s == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	root, err := s.safePath(prefix)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, errors.New("image task cleanup prefix is not a directory")
	}
	deleted := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if deleted >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		// A sidecar is deleted with its payload; processing it independently
		// would count the same artifact twice and could leave a dangling image.
		if strings.HasSuffix(entry.Name(), ".meta") || !entry.Type().IsRegular() {
			return nil
		}
		fileInfo, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if !fileInfo.ModTime().Before(before) {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		metaPath := path + ".meta"
		if removeErr := os.Remove(metaPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		deleted++
		return nil
	})
	return deleted, err
}

func NewLocalImageTaskObjectStore(root string, signingKey []byte, publicPath string) (*LocalImageTaskObjectStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("local image task storage root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if len(signingKey) < 32 {
		return nil, errors.New("local image task download signing key must contain at least 32 bytes")
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("create local image task storage: %w", err)
	}
	if strings.TrimSpace(publicPath) == "" {
		publicPath = "/v1/image-tasks/objects"
	}
	return &LocalImageTaskObjectStore{root: abs, signingKey: append([]byte(nil), signingKey...), publicPath: publicPath}, nil
}

func (s *LocalImageTaskObjectStore) safePath(key string) (string, error) {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
		return "", errors.New("invalid image task object key")
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	path := filepath.Join(s.root, clean)
	rel, err := filepath.Rel(s.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("image task object key escapes storage root")
	}
	return path, nil
}

func (s *LocalImageTaskObjectStore) Put(_ context.Context, key string, body io.Reader, contentType string, maxBytes int64) (int64, string, error) {
	if body == nil || maxBytes <= 0 {
		return 0, "", errors.New("image task object body and size limit are required")
	}
	path, err := s.safePath(key)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".image-task-*")
	if err != nil {
		return 0, "", err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(body, maxBytes+1))
	if err != nil {
		return 0, "", err
	}
	if written > maxBytes {
		return 0, "", fmt.Errorf("image task object exceeds %d bytes", maxBytes)
	}
	if err := temp.Sync(); err != nil {
		return 0, "", err
	}
	if err := temp.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Chmod(tempPath, 0o640); err != nil {
		return 0, "", err
	}
	// Rename is atomic on the same volume. Replaying a task safely replaces the
	// deterministic object key instead of leaving duplicate files behind.
	if err := os.Rename(tempPath, path); err != nil {
		return 0, "", err
	}
	committed = true
	meta := []byte(strings.TrimSpace(contentType) + "\n" + strconv.FormatInt(written, 10))
	if err := os.WriteFile(path+".meta", meta, 0o640); err != nil {
		_ = os.Remove(path)
		return 0, "", err
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *LocalImageTaskObjectStore) Open(_ context.Context, key string) (io.ReadCloser, string, int64, error) {
	path, err := s.safePath(key)
	if err != nil {
		return nil, "", 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", 0, err
	}
	contentType := "application/octet-stream"
	if meta, readErr := os.ReadFile(path + ".meta"); readErr == nil {
		if first, _, ok := strings.Cut(string(meta), "\n"); ok && strings.TrimSpace(first) != "" {
			contentType = strings.TrimSpace(first)
		}
	}
	return file, contentType, info.Size(), nil
}

func (s *LocalImageTaskObjectStore) Delete(_ context.Context, key string) error {
	path, err := s.safePath(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	metaErr := os.Remove(path + ".meta")
	if metaErr != nil && !errors.Is(metaErr, os.ErrNotExist) {
		return metaErr
	}
	return nil
}

func (s *LocalImageTaskObjectStore) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	if _, err := s.safePath(key); err != nil {
		return "", err
	}
	// Local files are never exposed by path. The service converts this marker
	// into an authenticated task/artifact route, which re-checks task ownership
	// on every download. S3 implementations can safely return a presigned URL.
	return "local-image-task://" + url.PathEscape(key), nil
}

func (s *LocalImageTaskObjectStore) VerifySignedURL(key string, expires int64, signature string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if expires <= now.Unix() || expires > now.Add(24*time.Hour).Unix() {
		return errors.New("image task download URL is expired or invalid")
	}
	want, err := hex.DecodeString(s.sign(key, expires))
	if err != nil {
		return err
	}
	got, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || !hmac.Equal(want, got) {
		return errors.New("image task download signature is invalid")
	}
	return nil
}

func (s *LocalImageTaskObjectStore) sign(key string, expires int64) string {
	mac := hmac.New(sha256.New, s.signingKey)
	_, _ = io.WriteString(mac, key)
	_, _ = io.WriteString(mac, "\n")
	_, _ = io.WriteString(mac, strconv.FormatInt(expires, 10))
	return hex.EncodeToString(mac.Sum(nil))
}

type S3ImageTaskObjectStore struct {
	client *s3.Client
	bucket string
	prefix string
}

// DeletePrefixOlderThan removes old objects in bounded pages. It is used for
// synchronous media cleanup and deliberately scopes every listing to the
// caller-provided namespace, so asynchronous task artifacts are untouched.
func (s *S3ImageTaskObjectStore) DeletePrefixOlderThan(ctx context.Context, prefix string, before time.Time, limit int) (int, error) {
	if s == nil || s.client == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	cleanPrefix := strings.Trim(strings.TrimSpace(strings.ReplaceAll(prefix, "\\", "/")), "/")
	if cleanPrefix == "" || strings.Contains(cleanPrefix, "..") {
		return 0, errors.New("invalid image task S3 cleanup prefix")
	}
	listPrefix := cleanPrefix + "/"
	if s.prefix != "" {
		listPrefix = s.prefix + "/" + listPrefix
	}
	deleted := 0
	var continuation *string
	for deleted < limit {
		input := &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &listPrefix, ContinuationToken: continuation}
		page, err := s.client.ListObjectsV2(ctx, input)
		if err != nil {
			return deleted, err
		}
		for _, object := range page.Contents {
			if deleted >= limit || object.Key == nil || object.LastModified == nil || !object.LastModified.Before(before) {
				continue
			}
			if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: object.Key}); err != nil {
				return deleted, err
			}
			deleted++
		}
		if page.IsTruncated == nil || !*page.IsTruncated || page.NextContinuationToken == nil || strings.TrimSpace(*page.NextContinuationToken) == "" {
			break
		}
		continuation = page.NextContinuationToken
	}
	return deleted, nil
}

func NewS3ImageTaskObjectStore(ctx context.Context, cfg *service.BackupS3Config) (*S3ImageTaskObjectStore, error) {
	if cfg == nil || !cfg.IsConfigured() {
		return nil, errors.New("image task S3 storage is not configured")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load image task S3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if strings.TrimSpace(cfg.Endpoint) != "" {
			options.BaseEndpoint = &cfg.Endpoint
		}
		options.UsePathStyle = cfg.ForcePathStyle
		options.APIOptions = append(options.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		options.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	return &S3ImageTaskObjectStore{client: client, bucket: cfg.Bucket, prefix: strings.Trim(strings.TrimSpace(cfg.Prefix), "/")}, nil
}

func (s *S3ImageTaskObjectStore) objectKey(key string) (string, error) {
	key = strings.Trim(strings.TrimSpace(strings.ReplaceAll(key, "\\", "/")), "/")
	if key == "" || strings.Contains(key, "..") {
		return "", errors.New("invalid image task S3 object key")
	}
	if s.prefix == "" {
		return key, nil
	}
	return s.prefix + "/" + key, nil
}

func (s *S3ImageTaskObjectStore) Put(ctx context.Context, key string, body io.Reader, contentType string, maxBytes int64) (int64, string, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return 0, "", err
	}
	if body == nil || maxBytes <= 0 {
		return 0, "", errors.New("image task object body and size limit are required")
	}
	// The AWS SDK retries a seekable file safely. Spooling through a bounded
	// temporary file keeps heap use independent of workers*max_artifact_bytes;
	// a 64-worker node no longer needs to hold 64 complete images in memory.
	temp, err := os.CreateTemp("", ".image-task-s3-*")
	if err != nil {
		return 0, "", err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(body, maxBytes+1))
	if err != nil {
		return 0, "", err
	}
	if written > maxBytes {
		return 0, "", fmt.Errorf("image task object exceeds %d bytes", maxBytes)
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &objectKey, Body: temp, ContentLength: aws.Int64(written), ContentType: &contentType,
		Metadata: map[string]string{"sha256": digest},
	})
	if err != nil {
		return 0, "", fmt.Errorf("put image task S3 object: %w", err)
	}
	return written, digest, nil
}

func (s *S3ImageTaskObjectStore) Open(ctx context.Context, key string) (io.ReadCloser, string, int64, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return nil, "", 0, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &objectKey})
	if err != nil {
		return nil, "", 0, err
	}
	contentType := "application/octet-stream"
	if result.ContentType != nil && strings.TrimSpace(*result.ContentType) != "" {
		contentType = *result.ContentType
	}
	var size int64
	if result.ContentLength != nil {
		size = *result.ContentLength
	}
	return result.Body, contentType, size, nil
}

func (s *S3ImageTaskObjectStore) Delete(ctx context.Context, key string) error {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &objectKey})
	return err
}

func (s *S3ImageTaskObjectStore) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return "", err
	}
	result, err := s3.NewPresignClient(s.client).PresignGetObject(ctx,
		&s3.GetObjectInput{Bucket: &s.bucket, Key: &objectKey}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign image task S3 object: %w", err)
	}
	return result.URL, nil
}
