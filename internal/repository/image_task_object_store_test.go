package repository

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocalImageTaskObjectStoreRoundTripAndSignature(t *testing.T) {
	store, err := NewLocalImageTaskObjectStore(t.TempDir(), []byte("01234567890123456789012345678901"), "")
	require.NoError(t, err)

	size, digest, err := store.Put(context.Background(), "image-tasks/task/00", strings.NewReader("image"), "image/png", 10)
	require.NoError(t, err)
	require.Equal(t, int64(5), size)
	require.NotEmpty(t, digest)

	body, contentType, openedSize, err := store.Open(context.Background(), "image-tasks/task/00")
	require.NoError(t, err)
	defer body.Close()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "image", string(data))
	require.Equal(t, "image/png", contentType)
	require.Equal(t, size, openedSize)

	signed, err := store.SignedURL(context.Background(), "image-tasks/task/00", time.Minute)
	require.NoError(t, err)
	require.Contains(t, signed, "local-image-task://")
}

func TestLocalImageTaskObjectStoreRejectsTraversalAndOversize(t *testing.T) {
	store, err := NewLocalImageTaskObjectStore(t.TempDir(), []byte("01234567890123456789012345678901"), "")
	require.NoError(t, err)
	_, _, err = store.Put(context.Background(), "../outside", strings.NewReader("x"), "image/png", 10)
	require.Error(t, err)
	_, _, err = store.Put(context.Background(), "image-tasks/task/00", strings.NewReader("too-large"), "image/png", 2)
	require.Error(t, err)
}

func TestLocalImageTaskObjectStoreDeletesOnlyExpiredPrefix(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalImageTaskObjectStore(root, []byte("01234567890123456789012345678901"), "")
	require.NoError(t, err)
	_, _, err = store.Put(context.Background(), "image-sync/old/0000", strings.NewReader("old"), "image/png", 10)
	require.NoError(t, err)
	_, _, err = store.Put(context.Background(), "image-sync/new/0000", strings.NewReader("new"), "image/png", 10)
	require.NoError(t, err)
	_, _, err = store.Put(context.Background(), "image-tasks/keep/0000", strings.NewReader("keep"), "image/png", 10)
	require.NoError(t, err)
	oldPath := filepath.Join(root, "image-sync", "old", "0000")
	require.NoError(t, os.Chtimes(oldPath, time.Unix(1, 0), time.Unix(1, 0)))
	deleted, err := store.DeletePrefixOlderThan(context.Background(), "image-sync", time.Unix(2, 0), 10)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	_, err = os.Stat(oldPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(root, "image-sync", "new", "0000"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "image-tasks", "keep", "0000"))
	require.NoError(t, err)
}
