package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageLogSessionMigrationIsNullableAndBounded(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "migrations", "179_add_usage_log_session_id.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := strings.ToLower(string(payload))
	if !strings.Contains(sqlText, "add column if not exists session_id varchar(255)") {
		t.Fatalf("migration must add bounded session_id column")
	}
	if strings.Contains(sqlText, "session_id varchar(255) not null") {
		t.Fatalf("usage_logs.session_id must remain nullable for existing rows")
	}
}
