package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestBuildOpsErrorLogsWhere_QueryUsesQualifiedColumns(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		Query: "ACCESS_DENIED",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "e.request_id ILIKE $") {
		t.Fatalf("where should include qualified request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.client_request_id ILIKE $") {
		t.Fatalf("where should include qualified client_request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.error_message ILIKE $") {
		t.Fatalf("where should include qualified error_message condition: %s", where)
	}
}

func TestBuildOpsErrorLogsWhere_UserQueryUsesExistsSubquery(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		UserQuery: "admin@",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM users u WHERE u.id = e.user_id AND u.email ILIKE $") {
		t.Fatalf("where should include EXISTS user email condition: %s", where)
	}
}

func TestBuildOpsErrorLogsOrderByWhitelist(t *testing.T) {
	tests := []struct {
		name string
		sort string
		want string
	}{
		{name: "default", sort: "", want: "ORDER BY e.created_at DESC, e.id DESC"},
		{name: "created asc", sort: "created_at_asc", want: "ORDER BY e.created_at ASC, e.id ASC"},
		{name: "status desc", sort: "status_code_desc", want: "COALESCE(e.upstream_status_code, e.status_code, 0) DESC"},
		{name: "severity desc", sort: "severity_desc", want: "CASE UPPER(COALESCE(e.severity, ''))"},
		{name: "invalid fallback", sort: "e.created_at;DROP TABLE ops_error_logs", want: "ORDER BY e.created_at DESC, e.id DESC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildOpsErrorLogsOrderBy(tt.sort)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("sort %q order by = %q, want containing %q", tt.sort, got, tt.want)
			}
			if strings.Contains(got, ";") {
				t.Fatalf("order by should never contain raw SQL separator: %q", got)
			}
		})
	}
}
