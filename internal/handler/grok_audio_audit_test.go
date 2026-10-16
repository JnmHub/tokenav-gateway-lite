package handler

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExtractGrokSTTRequestAuditBodySkipsFileBytes(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "safe transcription context"))
	file, err := writer.CreateFormFile("file", "voice.wav")
	require.NoError(t, err)
	_, err = file.Write([]byte("forbidden words inside binary audio bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	auditBody := extractGrokSTTRequestAuditBody(body.Bytes(), writer.FormDataContentType())
	require.Contains(t, string(auditBody), "safe transcription context")
	require.NotContains(t, string(auditBody), "forbidden words inside binary audio bytes")
}

func TestExtractGrokSTTResponseAuditBodyUsesTranscriptFieldsOnly(t *testing.T) {
	auditBody := extractGrokSTTResponseAuditBody([]byte(`{
		"text":"first transcript",
		"transcription":"second transcript",
		"audio":"forbidden base64 payload"
	}`))

	require.Contains(t, string(auditBody), "first transcript")
	require.Contains(t, string(auditBody), "second transcript")
	require.NotContains(t, string(auditBody), "forbidden base64 payload")
}

func TestExtractGrokRealtimeAuditBodyIgnoresAudioBase64(t *testing.T) {
	auditBody := extractGrokRealtimeAuditBody([]byte(`{
		"type":"conversation.item.create",
		"instructions":"follow safe instructions",
		"item":{"content":[
			{"type":"input_audio","audio":"forbidden-base64"},
			{"type":"input_text","text":"current user text"}
		]}
	}`))

	require.Equal(t, "follow safe instructions", gjson.GetBytes(auditBody, "messages.0.content").String())
	require.Equal(t, "current user text", gjson.GetBytes(auditBody, "messages.1.content").String())
	require.NotContains(t, string(auditBody), "forbidden-base64")
}

func TestExtractGrokRealtimeAuditBodyDoesNotScanUnrelatedHistory(t *testing.T) {
	auditBody := extractGrokRealtimeAuditBody([]byte(`{
		"type":"response.create",
		"input":"safe current event",
		"history":[{"role":"user","content":"forbidden historical event"}]
	}`))

	require.Contains(t, string(auditBody), "safe current event")
	require.NotContains(t, string(auditBody), "forbidden historical event")
}
