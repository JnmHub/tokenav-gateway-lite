package admin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseCodexAgentIdentityDocument(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"auth_mode": "agentIdentity",
		"agent_identity": map[string]any{
			"agent_runtime_id":           "runtime-1",
			"agent_private_key":          base64.StdEncoding.EncodeToString(der),
			"task_id":                    "task-1",
			"account_id":                 "account-1",
			"chatgpt_user_id":            "user-1",
			"email":                      "agent@example.invalid",
			"plan_type":                  "pro",
			"chatgpt_account_is_fedramp": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	doc, err := parseCodexAgentIdentityDocument(string(payload))
	if err != nil {
		t.Fatalf("parseCodexAgentIdentityDocument error = %v", err)
	}
	if doc.RuntimeID != "runtime-1" || doc.TaskID != "task-1" || doc.AccountID != "account-1" || doc.UserID != "user-1" {
		t.Fatalf("unexpected document: %#v", doc)
	}
	if !doc.FedRAMP {
		t.Fatal("FedRAMP = false, want true")
	}
}

func TestParseCodexAgentIdentityDocumentRejectsOrdinaryOAuth(t *testing.T) {
	_, err := parseCodexAgentIdentityDocument(`{"auth_mode":"oauth","tokens":{"access_token":"secret"}}`)
	if err == nil {
		t.Fatal("error = nil, want ordinary OAuth document rejection")
	}
}

func TestParseCodexAgentIdentityDocumentRejectsInvalidPrivateKey(t *testing.T) {
	_, err := parseCodexAgentIdentityDocument(`{
		"auth_mode":"agentIdentity",
		"agent_identity":{
			"agent_runtime_id":"runtime-1",
			"agent_private_key":"not-a-pkcs8-key",
			"account_id":"account-1",
			"chatgpt_user_id":"user-1"
		}
	}`)
	if err == nil {
		t.Fatal("error = nil, want invalid private key rejection")
	}
}

func TestValidateCodexAgentIdentityImportOptions(t *testing.T) {
	invalid := -1
	if err := validateCodexAgentIdentityImportOptions(CodexAgentIdentityImportRequest{Concurrency: &invalid}); err == nil {
		t.Fatal("negative concurrency must be rejected")
	}
	tooLarge := 10001
	if err := validateCodexAgentIdentityImportOptions(CodexAgentIdentityImportRequest{LoadFactor: &tooLarge}); err == nil {
		t.Fatal("load factor above 10000 must be rejected")
	}
}

func TestSanitizeCodexImportCredentialExtrasRejectsAllAuthenticationMaterial(t *testing.T) {
	input := map[string]any{
		"model_mapping":     map[string]any{"gpt-client": "gpt-upstream"},
		"compact_mapping":   map[string]any{"gpt-client": "gpt-compact"},
		"auth_mode":         "oauth",
		"agent_runtime_id":  "runtime-override",
		"agent_private_key": "private-override",
		"task_id":           "task-override",
		"access_token":      "access-override",
		"refresh_token":     "refresh-override",
		"api_key":           "api-key-override",
		"cookie":            "cookie-override",
		"session_key":       "session-override",
		"client_secret":     "client-secret-override",
	}

	got := sanitizeCodexImportCredentialExtras(input)
	if _, ok := got["model_mapping"]; !ok {
		t.Fatal("model_mapping should be preserved")
	}
	if _, ok := got["compact_mapping"]; !ok {
		t.Fatal("compact_mapping should be preserved")
	}
	for _, key := range []string{
		"auth_mode", "agent_runtime_id", "agent_private_key", "task_id",
		"access_token", "refresh_token", "api_key", "cookie", "session_key", "client_secret",
	} {
		if _, ok := got[key]; ok {
			t.Fatalf("%s must be removed from credential_extras", key)
		}
	}
}
