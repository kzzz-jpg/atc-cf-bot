package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/config"
)

func TestCompletionValidationAndRequest(t *testing.T) {
	valid := `{"choices":[{"message":{"content":"答案"},"finish_reason":"stop"}]}`
	cases := []struct {
		name, body string
		ok         bool
	}{{"valid", valid, true}, {"empty", `{"choices":[]}`, false}, {"length", `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`, false}, {"refusal", `{"choices":[{"message":{"content":"no","refusal":"policy"},"finish_reason":"stop"}]}`, false}, {"tools", `{"choices":[{"message":{"content":"text","tool_calls":[{}]},"finish_reason":"stop"}]}`, false}, {"missing_finish", `{"choices":[{"message":{"content":"text"}}]}`, false}, {"HTML", "<html>error</html>", false}, {"truncated", valid[:len(valid)-3], false}, {"multiple_json", valid + valid, false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("wrong request")
				}
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if payload["temperature"] != nil || payload["stream"] != false || payload["max_completion_tokens"] == nil || payload["tools"] != nil {
					t.Errorf("wrong payload %#v", payload)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c := config.Default().LLM
			c.BaseURL = server.URL + "/v1/"
			c.Model = "test"
			c.APIKey = "secret"
			client := New(c, 4<<20)
			_, e := client.Complete(context.Background(), Request{Kind: "hint", Messages: []Message{{Role: "user", Content: "題目"}}})
			if (e == nil) != tc.ok || calls != 1 {
				t.Fatalf("result=%v calls=%d", e, calls)
			}
		})
	}
}
func TestRetryAndDisable(t *testing.T) {
	for _, status := range []int{401, 403, 400, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "SECRET_PROVIDER_BODY")
			}))
			defer server.Close()
			c := config.Default().LLM
			c.BaseURL = server.URL
			c.Model = "m"
			client := New(c, 1024)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, e := client.Complete(ctx, Request{Kind: "hint"})
			if e == nil {
				t.Fatal("error accepted")
			}
			if status < 429 {
				_, _ = client.Complete(ctx, Request{Kind: "hint"})
				if count.Load() != 1 {
					t.Fatal("bad config retried")
				}
			} else if count.Load() != 3 {
				t.Fatalf("retry budget: %d", count.Load())
			}
			if e.Error() == "SECRET_PROVIDER_BODY" {
				t.Fatal("body exposed")
			}
		})
	}
}
func TestRedirectAndCancelledBackoff(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	c := config.Default().LLM
	c.BaseURL = server.URL
	client := New(c, 1024)
	_, e := client.Complete(context.Background(), Request{Kind: "hint"})
	if e == nil || leaked.Load() {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if Wait(ctx, time.Hour) == nil || time.Since(started) > time.Second {
		t.Fatal("backoff not cancellable")
	}
}
func TestFingerprint(t *testing.T) {
	c := config.Default().LLM
	c.Model = "a"
	client := New(c, 1024)
	req := Request{Kind: "hint"}
	fp := client.Fingerprint(req, "s", "p", "cpp17")
	raw, _ := json.Marshal(fp)
	c.APIKey = "another-key"
	client.Config = c
	other, _ := json.Marshal(client.Fingerprint(req, "s", "p", "cpp17"))
	if string(raw) != string(other) {
		t.Fatal("key changed fingerprint")
	}
	client.Config.Model = "b"
	other, _ = json.Marshal(client.Fingerprint(req, "s", "p", "cpp17"))
	if string(raw) == string(other) {
		t.Fatal("model did not change fingerprint")
	}
}

func TestExplicitContextLimitDoesNotDisableModel(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"context_length_exceeded","message":"SECRET_PROVIDER_BODY"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"short input answer"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := config.Default().LLM
	cfg.BaseURL = server.URL
	cfg.Model = "mock"
	client := New(cfg, 4096)
	if _, e := client.Complete(context.Background(), Request{Kind: "translation"}); e == nil || e.Error() != "context_too_long" {
		t.Fatal("wrong context error classification", e)
	}
	if client.Degraded() {
		t.Fatal("one large prompt disabled model")
	}
	if _, e := client.Complete(context.Background(), Request{Kind: "hint"}); e != nil || count.Load() != 2 {
		t.Fatal("valid smaller request could not run", e)
	}
}

func TestContextLimitClassificationRequiresBoundedStructuredEvidence(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"context too long"}}`, `<html>context_length_exceeded</html>`, `{"error":{"code":"invalid_model"}}`, `{"error":{"code":"context_length_exceeded"}} trailing`, strings.Repeat("x", 2000)} {
		if contextLengthExceeded(strings.NewReader(body), 1024) {
			t.Fatal("unverified context evidence accepted")
		}
	}
}
