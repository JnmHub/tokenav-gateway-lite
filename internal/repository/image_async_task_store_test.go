package repository

import (
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDecodeImageTaskCursorValidatesEmbeddedUUID(t *testing.T) {
	validID := uuid.NewString()
	valid := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", time.Now().UnixNano(), validID)))
	_, decodedID, err := decodeImageTaskCursor(valid)
	require.NoError(t, err)
	require.Equal(t, validID, decodedID)

	malicious := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:not-a-uuid", time.Now().UnixNano())))
	_, _, err = decodeImageTaskCursor(malicious)
	require.ErrorIs(t, err, service.ErrImageTaskInvalidInput)
}

func TestImageTaskUUIDValidationRejectsDatabaseCastInputs(t *testing.T) {
	require.True(t, validImageTaskID(uuid.NewString()))
	for _, value := range []string{"", "not-a-uuid", "1'::uuid", "00000000"} {
		require.False(t, validImageTaskID(value), value)
	}

	// A nil store still proves invalid input is rejected before a SQL call can
	// turn PostgreSQL's UUID cast error into a 500 response.
	store := &imageAsyncTaskStore{}
	_, err := store.Get(nil, "not-a-uuid")
	require.True(t, errors.Is(err, service.ErrImageTaskNotFound))
}
