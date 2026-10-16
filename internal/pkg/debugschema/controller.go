package debugschema

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Controller keeps a short-lived in-memory ring of request/response shapes.
// Values are never retained; this package is intentionally independent from
// the database-backed operations log so a debug session cannot leak payloads.
type Controller struct {
	mu           sync.Mutex
	enabledUntil time.Time
	maxEntries   int
	scope        Scope
	items        []Snapshot
}

var defaultController = &Controller{maxEntries: 100, scope: Scope{Type: "all"}}

func DefaultController() *Controller { return defaultController }

type HeaderShape struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type FieldShape struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Length *int   `json:"length,omitempty"`
}
type BodyShape struct {
	ContentType string       `json:"content_type,omitempty"`
	Bytes       int          `json:"bytes"`
	Truncated   bool         `json:"truncated,omitempty"`
	Fields      []FieldShape `json:"fields,omitempty"`
}
type Snapshot struct {
	ID              string        `json:"id"`
	CreatedAt       time.Time     `json:"created_at"`
	DurationMs      int64         `json:"duration_ms"`
	Method          string        `json:"method"`
	Path            string        `json:"path"`
	RequestID       string        `json:"request_id,omitempty"`
	APIKeyID        int64         `json:"api_key_id,omitempty"`
	APIKeyRef       string        `json:"api_key_ref,omitempty"`
	GroupID         int64         `json:"group_id,omitempty"`
	GroupName       string        `json:"group_name,omitempty"`
	RequestHeaders  []HeaderShape `json:"request_headers"`
	RequestBody     *BodyShape    `json:"request_body,omitempty"`
	ResponseStatus  int           `json:"response_status"`
	ResponseHeaders []HeaderShape `json:"response_headers"`
	ResponseBody    *BodyShape    `json:"response_body,omitempty"`
}

// Scope limits collection to all requests, one API key, one group or a path
// prefix. Values are IDs/prefixes only; secret key material is never stored.
type Scope struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

func (c *Controller) Enable(until time.Time, maxEntries int) {
	c.EnableWithScope(until, maxEntries, Scope{Type: "all"})
}

func (c *Controller) EnableWithScope(until time.Time, maxEntries int, scope Scope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxEntries < 1 || maxEntries > 500 {
		maxEntries = 100
	}
	c.maxEntries = maxEntries
	c.scope = NormalizeScope(scope)
	c.enabledUntil = until
	c.items = nil
}
func (c *Controller) Disable() {
	c.mu.Lock()
	c.enabledUntil = time.Time{}
	c.scope = Scope{Type: "all"}
	c.items = nil
	c.mu.Unlock()
}
func (c *Controller) Clear() { c.mu.Lock(); c.items = nil; c.mu.Unlock() }
func (c *Controller) Enabled(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabledUntil.After(now)
}
func (c *Controller) Status(now time.Time) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scope = NormalizeScope(c.scope)
	enabled := c.enabledUntil.After(now)
	if !enabled && !c.enabledUntil.IsZero() {
		c.enabledUntil = time.Time{}
		c.scope = Scope{Type: "all"}
		c.items = nil
	}
	ttl := int64(0)
	if enabled {
		ttl = int64(time.Until(c.enabledUntil).Seconds())
		if ttl < 0 {
			ttl = 0
		}
	}
	var expires any
	if enabled {
		expires = c.enabledUntil.UTC().Format(time.RFC3339)
	}
	return map[string]any{"enabled": enabled, "expires_at": expires, "ttl_seconds": ttl, "count": len(c.items), "max_records": c.maxEntries, "scope": c.scope}
}
func (c *Controller) Add(s Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabledUntil.After(time.Now()) {
		return
	}
	if !matchesScope(c.scope, s) {
		return
	}
	if c.maxEntries < 1 {
		c.maxEntries = 100
	}
	if len(c.items) >= c.maxEntries {
		copy(c.items, c.items[1:])
		c.items = c.items[:len(c.items)-1]
	}
	c.items = append(c.items, s)
}

func NormalizeScope(scope Scope) Scope {
	scope.Type = strings.ToLower(strings.TrimSpace(scope.Type))
	scope.Value = strings.TrimSpace(scope.Value)
	switch scope.Type {
	case "group", "api_key", "path":
		if scope.Value != "" {
			return scope
		}
	}
	return Scope{Type: "all"}
}

func matchesScope(scope Scope, s Snapshot) bool {
	scope = NormalizeScope(scope)
	switch scope.Type {
	case "group":
		return strconv.FormatInt(s.GroupID, 10) == scope.Value
	case "api_key":
		return (s.APIKeyID > 0 && strconv.FormatInt(s.APIKeyID, 10) == scope.Value) || strings.TrimSpace(s.APIKeyRef) == scope.Value
	case "path":
		return strings.HasPrefix(s.Path, scope.Value)
	default:
		return true
	}
}
func (c *Controller) List() []Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Snapshot, len(c.items))
	copy(out, c.items)
	return out
}

func Headers(h http.Header) []HeaderShape {
	out := make([]HeaderShape, 0, len(h))
	for k, v := range h {
		out = append(out, HeaderShape{Name: k, Count: len(v)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Shape parses JSON and emits only paths/types. Non-JSON bodies retain only
// content type and byte count. Limits prevent malicious nesting from growing
// the temporary diagnostic buffer without bound.
func Shape(body []byte, contentType string, truncated bool) *BodyShape {
	b := &BodyShape{ContentType: strings.TrimSpace(strings.Split(contentType, ";")[0]), Bytes: len(body), Truncated: truncated}
	var value any
	if len(body) == 0 || json.Unmarshal(body, &value) != nil {
		return b
	}
	fields := make([]FieldShape, 0, 64)
	walkShape(&fields, "", value, 0)
	b.Fields = fields
	return b
}
func walkShape(out *[]FieldShape, path string, v any, depth int) {
	if depth > 12 || len(*out) >= 256 {
		return
	}
	typ := "null"
	var n *int
	switch x := v.(type) {
	case map[string]any:
		typ = "object"
		*out = append(*out, FieldShape{Path: path, Type: typ})
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walkShape(out, p, x[k], depth+1)
		}
		return
	case []any:
		typ = "array"
		l := len(x)
		n = &l
		*out = append(*out, FieldShape{Path: path, Type: typ, Length: n})
		for i := 0; i < len(x) && i < 16; i++ {
			p := stringifyIndex(path, i)
			walkShape(out, p, x[i], depth+1)
		}
		return
	case string:
		typ = "string"
	case float64:
		typ = "number"
	case bool:
		typ = "boolean"
	}
	*out = append(*out, FieldShape{Path: path, Type: typ})
}
func stringifyIndex(path string, i int) string {
	if path == "" {
		return "[" + strconv.Itoa(i) + "]"
	}
	return path + "[" + strconv.Itoa(i) + "]"
}

// CopyBody returns a bounded copy suitable for response inspection.
func CopyBody(b []byte, limit int) ([]byte, bool) {
	if limit < 1 {
		return nil, len(b) > 0
	}
	if len(b) <= limit {
		return append([]byte(nil), b...), false
	}
	return append([]byte(nil), b[:limit]...), true
}
