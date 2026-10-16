package debugschema

import (
	"strings"
	"testing"
	"time"
)

func TestShapeContainsStructureOnly(t *testing.T) {
	shape := Shape([]byte(`{"model":"secret-model","messages":[{"role":"user","content":"private prompt"}]}`), "application/json", false)
	if len(shape.Fields) == 0 {
		t.Fatal("expected fields")
	}
	for _, field := range shape.Fields {
		if strings.Contains(field.Path, "secret-model") || strings.Contains(field.Path, "private prompt") {
			t.Fatalf("payload value leaked in path: %+v", field)
		}
	}
}

func TestControllerExpiresAndClears(t *testing.T) {
	c := &Controller{}
	c.Enable(time.Now().Add(time.Minute), 2)
	c.Add(Snapshot{ID: "one"})
	if got := len(c.List()); got != 1 {
		t.Fatalf("got %d records", got)
	}
	c.Enable(time.Now().Add(-time.Second), 2)
	if got := c.Status(time.Now())["count"]; got != 0 {
		t.Fatalf("expired controller retained records: %v", got)
	}
}

func TestControllerScopeFiltersRecords(t *testing.T) {
	c := &Controller{}
	c.EnableWithScope(time.Now().Add(time.Minute), 10, Scope{Type: "group", Value: "42"})
	c.Add(Snapshot{ID: "match", GroupID: 42, APIKeyID: 7})
	c.Add(Snapshot{ID: "other", GroupID: 99, APIKeyID: 7})
	items := c.List()
	if len(items) != 1 || items[0].ID != "match" {
		t.Fatalf("unexpected scoped records: %+v", items)
	}
	c.EnableWithScope(time.Now().Add(time.Minute), 10, Scope{Type: "api_key", Value: "7"})
	c.Add(Snapshot{ID: "key-match", GroupID: 1, APIKeyID: 7})
	c.Add(Snapshot{ID: "key-other", GroupID: 1, APIKeyID: 8})
	if items := c.List(); len(items) != 1 || items[0].ID != "key-match" {
		t.Fatalf("unexpected key-scoped records: %+v", items)
	}
}

func TestNormalizeScopeEmptyFallsBackToAll(t *testing.T) {
	if got := NormalizeScope(Scope{Type: "group"}); got.Type != "all" {
		t.Fatalf("got %#v", got)
	}
}
