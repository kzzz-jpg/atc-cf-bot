package generation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/prompts"
)

func TestTranslationBatchesKeepBackgroundAndByteBound(t *testing.T) {
	cfg := config.Default().LLM
	cfg.Model = "mock"
	system := prompts.Get("translation")
	b := domain.Block{ID: "body", Kind: "statement", Text: strings.Repeat("term variable paragraph. ", 180)}
	st := domain.Statement{Title: "Terms", Blocks: []domain.Block{b, {ID: "input", Kind: "input", Text: "Read variable N."}}}
	background, e := translationBackground(st, 2048)
	if e != nil {
		t.Fatal(e)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
			return
		}
		requests++
		user := req.Messages[1].Content
		if !strings.Contains(user, "Read variable N.") || len(user)+len(req.Messages[0].Content) > 4096 {
			t.Error("missing background or exceeded bound")
		}
		at := strings.LastIndex(user, "：\n")
		if at < 0 {
			t.Error("missing block delimiter")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": user[at+len("：\n"):]}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	cfg.BaseURL = server.URL
	s := Service{Client: llm.New(cfg, 4<<20), BlockLimit: 4096}
	translated, e := s.translate(context.Background(), b, system, background, 0)
	if e != nil || translated != b.Text || requests < 2 {
		t.Fatalf("split lost text: %v requests=%d", e, requests)
	}
	if _, e = translationBackground(domain.Statement{Blocks: []domain.Block{{Kind: "input", Text: strings.Repeat("x", 5000)}}}, 2048); e == nil {
		t.Fatal("oversized background silently truncated")
	}
}

func TestProviderContextLimitShrinksTranslationWithoutLosingText(t *testing.T) {
	cfg := config.Default().LLM
	cfg.Model = "mock"
	body := strings.Repeat("full original text. ", 200)
	tooLarge, requests := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
			return
		}
		user := req.Messages[1].Content
		requests++
		if len(user) > 2000 {
			tooLarge++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded"}}`))
			return
		}
		at := strings.LastIndex(user, "：\n")
		if at < 0 {
			t.Error("missing block")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": user[at+len("：\n"):]}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	cfg.BaseURL = server.URL
	s := Service{Client: llm.New(cfg, 4<<20), BlockLimit: 8192}
	translated, e := s.translate(context.Background(), domain.Block{ID: "b", Kind: "statement", Text: body}, prompts.Get("translation"), "same variables and terminology", 0)
	if e != nil || translated != body || tooLarge != 1 || requests < 3 || s.Client.Degraded() {
		t.Fatalf("context split failed: %v requests=%d rejected=%d", e, requests, tooLarge)
	}
}
