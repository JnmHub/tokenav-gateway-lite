package debugschema

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// FullCaptureController stores complete payloads only in process memory for one
// explicitly selected local API key. It never writes payloads to DB, Redis, or logs.
type FullCaptureController struct {
	mu            sync.Mutex
	enabledUntil  time.Time
	keyID         int64
	keyRef        string
	maxRecords    int
	maxBodyBytes  int
	maxTotalBytes int
	totalBytes    int
	items         []FullSnapshot
}

var defaultFullController = &FullCaptureController{maxRecords: 20, maxBodyBytes: 1 << 20, maxTotalBytes: 10 << 20}

func DefaultFullCaptureController() *FullCaptureController { return defaultFullController }

type FullSnapshot struct {
	ID                  string      `json:"id"`
	CreatedAt           time.Time   `json:"created_at"`
	DurationMs          int64       `json:"duration_ms"`
	Method              string      `json:"method"`
	Path                string      `json:"path"`
	RequestID           string      `json:"request_id,omitempty"`
	APIKeyID            int64       `json:"api_key_id,omitempty"`
	APIKeyRef           string      `json:"api_key_ref,omitempty"`
	GroupID             int64       `json:"group_id,omitempty"`
	GroupName           string      `json:"group_name,omitempty"`
	RequestHeaders      http.Header `json:"request_headers"`
	RequestBody         []byte      `json:"-"`
	RequestContentType  string      `json:"request_content_type,omitempty"`
	RequestTruncated    bool        `json:"request_truncated,omitempty"`
	ResponseStatus      int         `json:"response_status"`
	ResponseHeaders     http.Header `json:"response_headers"`
	ResponseBody        []byte      `json:"-"`
	ResponseContentType string      `json:"response_content_type,omitempty"`
	ResponseTruncated   bool        `json:"response_truncated,omitempty"`
}

func (c *FullCaptureController) Enable(until time.Time, keyID int64, keyRef string, maxRecords, maxBodyBytes, maxTotalBytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxRecords < 1 || maxRecords > 100 {
		maxRecords = 20
	}
	if maxBodyBytes < 1024 || maxBodyBytes > 8<<20 {
		maxBodyBytes = 1 << 20
	}
	if maxTotalBytes < maxBodyBytes || maxTotalBytes > 64<<20 {
		maxTotalBytes = 10 << 20
	}
	c.enabledUntil, c.keyID, c.keyRef = until, keyID, strings.TrimSpace(keyRef)
	c.maxRecords, c.maxBodyBytes, c.maxTotalBytes = maxRecords, maxBodyBytes, maxTotalBytes
	c.totalBytes, c.items = 0, nil
}
func (c *FullCaptureController) Disable() {
	c.mu.Lock()
	c.enabledUntil = time.Time{}
	c.keyID = 0
	c.keyRef = ""
	c.items = nil
	c.totalBytes = 0
	c.mu.Unlock()
}
func (c *FullCaptureController) Clear() { c.mu.Lock(); c.items = nil; c.totalBytes = 0; c.mu.Unlock() }
func (c *FullCaptureController) Enabled(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabledUntil.After(now)
}
func (c *FullCaptureController) BodyLimit() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxBodyBytes < 1024 {
		return 1 << 20
	}
	return c.maxBodyBytes
}
func (c *FullCaptureController) Status(now time.Time) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	enabled := c.enabledUntil.After(now)
	if !enabled && !c.enabledUntil.IsZero() {
		c.enabledUntil = time.Time{}
		c.items = nil
		c.totalBytes = 0
		c.keyID = 0
		c.keyRef = ""
	}
	var exp any
	ttl := int64(0)
	if enabled {
		exp = c.enabledUntil.UTC().Format(time.RFC3339)
		ttl = int64(time.Until(c.enabledUntil).Seconds())
		if ttl < 0 {
			ttl = 0
		}
	}
	return map[string]any{"enabled": enabled, "expires_at": exp, "ttl_seconds": ttl, "key_id": c.keyID, "key_ref": c.keyRef, "count": len(c.items), "max_records": c.maxRecords, "max_body_bytes": c.maxBodyBytes, "max_total_bytes": c.maxTotalBytes, "stored_bytes": c.totalBytes}
}
func (c *FullCaptureController) Matches(keyID int64, keyRef string, local bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !local || !c.enabledUntil.After(time.Now()) {
		return false
	}
	if c.keyID > 0 {
		return keyID == c.keyID
	}
	return c.keyRef != "" && strings.TrimSpace(keyRef) == c.keyRef
}
func (c *FullCaptureController) Add(s FullSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabledUntil.After(time.Now()) {
		return
	}
	size := len(s.RequestBody) + len(s.ResponseBody)
	if size > c.maxTotalBytes {
		return
	}
	for len(c.items) >= c.maxRecords || c.totalBytes+size > c.maxTotalBytes {
		if len(c.items) == 0 {
			return
		}
		c.totalBytes -= len(c.items[0].RequestBody) + len(c.items[0].ResponseBody)
		c.items = c.items[1:]
	}
	s.RequestBody = append([]byte(nil), s.RequestBody...)
	s.ResponseBody = append([]byte(nil), s.ResponseBody...)
	c.items = append(c.items, s)
	c.totalBytes += size
}
func (c *FullCaptureController) List() []FullSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]FullSnapshot, len(c.items))
	copy(out, c.items)
	return out
}

// PayloadForJSON exposes bytes without corrupting binary bodies. JSON/UTF-8 is
// returned as text; binary payloads are base64 encoded and explicitly labeled.
func PayloadForJSON(b []byte, contentType string, truncated bool) map[string]any {
	out := map[string]any{"content_type": strings.TrimSpace(strings.Split(contentType, ";")[0]), "bytes": len(b), "truncated": truncated}
	if utf8.Valid(b) {
		out["encoding"] = "utf-8"
		out["data"] = string(b)
	} else {
		out["encoding"] = "base64"
		out["data"] = base64.StdEncoding.EncodeToString(b)
	}
	return out
}
func CloneHeaderValues(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		vv := append([]string(nil), v...)
		if sensitiveHeader(k) {
			vv = []string{"[REDACTED]"}
		}
		out[k] = vv
	}
	return out
}
func sensitiveHeader(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	return k == "authorization" || k == "proxy-authorization" || k == "cookie" || k == "set-cookie" || strings.Contains(k, "api-key") || strings.Contains(k, "token") || strings.Contains(k, "secret")
}
func SortedHeaderNames(h http.Header) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
