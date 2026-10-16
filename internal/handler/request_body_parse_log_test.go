package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestLogRequestBodyParseFailureRecordsMetadataWithoutBody(t *testing.T) {
	core, observed := observer.New(zap.WarnLevel)
	log := zap.New(core)
	secret := "private-prompt-and-key"
	body := []byte("{\"input\":\"" + secret + "\",\"broken\":")

	logRequestBodyParseFailure(log, body, nil)

	entries := observed.All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.EqualValues(t, len(body), fields["body_len"])
	require.Contains(t, fields["error"], "invalid json")
	require.NotContains(t, entries[0].Message, secret)
	require.NotContains(t, fields["error"], secret)
	require.NotContains(t, fields, "body")
	require.NotContains(t, fields, "body_head")
	require.NotContains(t, fields, "body_tail")
}

func TestLogRequestBodyParseFailureNilLoggerDoesNotPanic(t *testing.T) {
	require.NotPanics(t, func() {
		logRequestBodyParseFailure(nil, []byte("{"), nil)
	})
}
