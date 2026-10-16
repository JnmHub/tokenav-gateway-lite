package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/debugschema"
	"github.com/gin-gonic/gin"
)

func TestRequestDebugSchemaRestoresBodyAndOmitsValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &debugschema.Controller{}
	c.Enable(time.Now().Add(time.Minute), 10)
	r := gin.New()
	r.Use(RequestDebugSchema(c))
	r.POST("/v1/test", func(ctx *gin.Context) {
		b, _ := io.ReadAll(ctx.Request.Body)
		ctx.JSON(http.StatusOK, gin.H{"echo": string(b), "ok": true})
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(`{"secret":"private-value"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	records := c.List()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	if !strings.Contains(resp.Body.String(), "private-value") {
		t.Fatal("handler did not receive restored body")
	}
	raw, _ := json.Marshal(records)
	if strings.Contains(string(raw), "private-value") {
		t.Fatalf("snapshot leaked payload value: %s", raw)
	}
}
