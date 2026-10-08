package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthReportsSafeReadiness(t *testing.T) {
	server := Health(context.Background(), "127.0.0.1:0", func() bool { return false }, func() bool { return true })
	defer server.Close()
	for _, tc := range []struct {
		path   string
		status int
	}{{"/livez", 200}, {"/readyz", 503}} {
		rec := httptest.NewRecorder()
		server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s status=%d", tc.path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "token") || strings.Contains(rec.Body.String(), "password") {
			t.Fatal("unsafe health")
		}
	}
	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	_ = server.Shutdown(ctx)
}
