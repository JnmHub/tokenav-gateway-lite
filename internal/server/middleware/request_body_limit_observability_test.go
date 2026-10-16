package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestBodyLimitPreserves413AndAggregatesKnownAndStreamingLengths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name          string
		contentLength int64
	}{
		{name: "known content length", contentLength: 5},
		{name: "streaming unknown length", contentLength: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			aggregator := ingress.NewAggregator()
			router := gin.New()
			router.Use(requestBodyLimit(4, aggregator))
			router.POST("/v1/responses", func(c *gin.Context) {
				_, err := io.ReadAll(c.Request.Body)
				if err != nil {
					var maxErr *http.MaxBytesError
					require.ErrorAs(t, err, &maxErr)
					c.Status(http.StatusRequestEntityTooLarge)
					return
				}
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("12345"))
			req.ContentLength = test.contentLength
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
			snapshot := aggregator.Snapshot(5)
			require.Equal(t, uint64(1), snapshot.RejectionCount)
			require.Equal(t, ingress.RejectBodyTooLarge, snapshot.TopReasons[0].Reason)
		})
	}
}

func TestRequestBodyLimitDoesNotClassifyHandler413AsIngressBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	aggregator := ingress.NewAggregator()
	router := gin.New()
	router.Use(requestBodyLimit(4, aggregator))
	router.POST("/v1/responses", func(c *gin.Context) {
		// 模拟合法请求到上游后返回 413；没有本地 MaxBytesError。
		c.Status(http.StatusRequestEntityTooLarge)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("1234")))

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Zero(t, aggregator.Health().RejectionsTotal)
}

func TestNestedRequestBodyLimitsAggregateOneRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	aggregator := ingress.NewAggregator()
	router := gin.New()
	router.Use(requestBodyLimit(8, aggregator))
	router.POST("/v1/alpha/search", requestBodyLimit(4, aggregator), func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		require.Error(t, err)
		c.Status(http.StatusRequestEntityTooLarge)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewBufferString("123456789")))

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Equal(t, uint64(1), aggregator.Snapshot(5).RejectionCount)
}
