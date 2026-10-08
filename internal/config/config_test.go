package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(k string) (string, bool) {
	v, ok := map[string]string{"DATABASE_URL": "postgres://test:test@localhost/test?sslmode=disable", "DATABASE_ALLOW_INSECURE_LOCAL": "true", "DISCORD_BOT_TOKEN": "dummy", "LLM_API_KEY": "dummy", "LLM_MODEL": "test"}[k]
	return v, ok
}
func TestStrictYAMLAndOverrides(t *testing.T) {
	dir := t.TempDir()
	for _, raw := range []string{"unknown: true\n", "llm:\n  model: a\n  model: b\n", "llm:\n  max_retries: -1\n", "llm:\n  temperature: .nan\n", "worker:\n  lease_seconds: 30\n", "llm: {}\n---\nllm: {}\n"} {
		p := filepath.Join(dir, "config.yaml")
		_ = os.WriteFile(p, []byte(raw), 0600)
		if _, e := Load(p, env, false); e == nil {
			t.Fatalf("bad config accepted: %s", raw)
		}
	}
	c, e := Load("", env, false)
	if e != nil || c.LLM.Model != "test" {
		t.Fatal(e)
	}
	_, e = Load("", func(k string) (string, bool) {
		if k == "LLM_MODEL" {
			return "", true
		}
		return env(k)
	}, false)
	if e == nil {
		t.Fatal("empty override accepted")
	}
}
func TestMigrationOnlyNeedsDB(t *testing.T) {
	_, e := Load("", func(k string) (string, bool) {
		if k == "DATABASE_URL" {
			return "postgres://test:test@localhost/db?sslmode=verify-full", true
		}
		return "", false
	}, true)
	if e != nil {
		t.Fatal(e)
	}
}
