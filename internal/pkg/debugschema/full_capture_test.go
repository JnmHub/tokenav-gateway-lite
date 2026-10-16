package debugschema

import (
	"net/http"
	"testing"
	"time"
)

func TestFullCaptureControllerMatchesOnlyLocalKeyAndBounds(t *testing.T) {
	c := &FullCaptureController{}
	c.Enable(time.Now().Add(time.Minute), 42, "", 2, 1024, 2048)
	if !c.Matches(42, "", true) {
		t.Fatal("configured local key should match")
	}
	if c.Matches(42, "", false) {
		t.Fatal("remote key must never match")
	}
	if c.Matches(7, "", true) {
		t.Fatal("different key must not match")
	}
	c.Add(FullSnapshot{RequestBody: []byte("a"), ResponseBody: []byte("b")})
	c.Add(FullSnapshot{RequestBody: []byte("c"), ResponseBody: []byte("d")})
	c.Add(FullSnapshot{RequestBody: []byte("e"), ResponseBody: []byte("f")})
	if got := len(c.List()); got != 2 {
		t.Fatalf("records=%d, want 2", got)
	}
}

func TestCloneHeaderValuesRedactsSecrets(t *testing.T) {
	h := CloneHeaderValues(http.Header{"Authorization": {"Bearer secret"}, "X-Debug": {"ok"}})
	if h.Get("Authorization") != "[REDACTED]" || h.Get("X-Debug") != "ok" {
		t.Fatalf("unexpected headers: %#v", h)
	}
}
