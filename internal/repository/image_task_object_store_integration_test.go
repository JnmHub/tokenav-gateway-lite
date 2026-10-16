//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const imageTaskMinIOImage = "minio/minio:RELEASE.2025-04-22T22-12-26Z"

func TestS3ImageTaskObjectStoreRealMinIORoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const (
		accessKey = "image-task-test-access"
		secretKey = "image-task-test-secret-0123456789"
		bucket    = "image-task-test"
	)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        imageTaskMinIOImage,
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     accessKey,
				"MINIO_ROOT_PASSWORD": secretKey,
			},
			Cmd:        []string{"server", "/data"},
			WaitingFor: wait.ForHTTP("/minio/health/live").WithPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start MinIO integration container: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = container.Terminate(cleanupCtx)
	})
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "9000/tcp")
	require.NoError(t, err)

	store, err := NewS3ImageTaskObjectStore(ctx, &service.BackupS3Config{
		Endpoint: fmt.Sprintf("http://%s:%s", host, port.Port()), Region: "us-east-1",
		Bucket: bucket, AccessKeyID: accessKey, SecretAccessKey: secretKey,
		Prefix: "integration", ForcePathStyle: true,
	})
	require.NoError(t, err)
	_, err = store.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: ptrString(bucket)})
	require.NoError(t, err)

	payload := []byte("real-minio-image-task-payload")
	wantHash := sha256.Sum256(payload)
	key := fmt.Sprintf("image-tasks/%d/00", time.Now().UnixNano())
	size, digest, err := store.Put(ctx, key, bytesReader(payload), "image/png", int64(len(payload))+1)
	require.NoError(t, err)
	require.EqualValues(t, len(payload), size)
	require.Equal(t, hex.EncodeToString(wantHash[:]), digest)

	body, contentType, openedSize, err := store.Open(ctx, key)
	require.NoError(t, err)
	opened, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, payload, opened)
	require.Equal(t, "image/png", contentType)
	require.Equal(t, size, openedSize)

	signedURL, err := store.SignedURL(ctx, key, time.Minute)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	downloaded, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, payload, downloaded)

	require.NoError(t, store.Delete(ctx, key))
	missingBody, _, _, err := store.Open(ctx, key)
	if missingBody != nil {
		_ = missingBody.Close()
	}
	require.Error(t, err)
}

func ptrString(value string) *string { return &value }

func bytesReader(value []byte) io.Reader { return &sliceReader{value: value} }

// sliceReader deliberately exposes only io.Reader. The production Put path
// must remain bounded even when an upstream artifact cannot seek or report its
// content length.
type sliceReader struct{ value []byte }

func (r *sliceReader) Read(buffer []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, r.value)
	r.value = r.value[n:]
	return n, nil
}
