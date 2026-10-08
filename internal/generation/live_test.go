package generation

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/source"
)

// This opt-in test generates paid/provider content. Ordinary tests always skip.
func TestLiveCompatibleLLM(t *testing.T) {
	base, model := os.Getenv("LIVE_LLM_BASE_URL"), os.Getenv("LIVE_LLM_MODEL")
	if base == "" || model == "" {
		t.Skip("set LIVE_LLM_BASE_URL and LIVE_LLM_MODEL to explicitly enable real generation")
	}
	cfg := config.Default().LLM
	cfg.BaseURL = base
	cfg.Model = model
	cfg.APIKey = os.Getenv("LIVE_LLM_API_KEY")
	if cfg.APIKey == "" {
		cfg.APIKey = "local-placeholder"
	}
	client := llm.New(cfg, 4<<20)
	defer client.HTTP.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if e := client.CheckConnectivity(ctx); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../testdata/codeforces.html")
	if e != nil {
		t.Fatal(e)
	}
	statement, e := source.Parse(domain.Codeforces, raw, "https://codeforces.com/problemset/problem/1/A", 64<<10)
	if e != nil {
		t.Fatal(e)
	}
	coordinator := New(ctx, &memoryCache{result: map[string]json.RawMessage{}}, 2, 20, 200, 50, 180*time.Second)
	defer func() { cancel(); coordinator.Close() }()
	service := Service{Client: client, Coordinator: coordinator, BlockLimit: 16 << 10}
	sid := domain.NewID()
	for _, kind := range []string{"translation", "hint", "solution"} {
		t.Run(kind, func(t *testing.T) {
			result, key, e := service.Generate(ctx, "live-test", sid, kind, "cpp17", statement)
			if e != nil {
				t.Fatal(e)
			}
			if len(result) == 0 || len(key) != 64 {
				t.Fatal("invalid generation result")
			}
			t.Logf("validated %s: %d bytes", kind, len(result))
			if dir := os.Getenv("LIVE_LLM_OUTPUT_DIR"); dir != "" {
				if e = os.MkdirAll(dir, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(dir+"/"+kind+".json", result, 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
