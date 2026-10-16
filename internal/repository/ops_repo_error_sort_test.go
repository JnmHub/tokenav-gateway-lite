package repository

import (
	"strings"
	"testing"
)

func TestBuildOpsErrorLogsOrderByCategory(t *testing.T) {
	for _, sort := range []string{"category_asc", "category_desc"} {
		orderBy := buildOpsErrorLogsOrderBy(sort)
		if !strings.Contains(orderBy, "error_phase") || !strings.Contains(orderBy, "error_type") {
			t.Fatalf("%s should sort by the stable category expression: %s", sort, orderBy)
		}
		if !strings.Contains(orderBy, "e.id") {
			t.Fatalf("%s should keep a stable id tiebreaker: %s", sort, orderBy)
		}
	}
}
