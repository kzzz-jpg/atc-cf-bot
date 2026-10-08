package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeManifestsAndPersistence(t *testing.T) {
	for _, name := range []string{"compose.yaml", "compose.local-llm.yaml"} {
		raw, e := os.ReadFile("../../" + name)
		if e != nil {
			t.Fatal(e)
		}
		var document map[string]any
		if yaml.Unmarshal(raw, &document) != nil {
			t.Fatal("invalid Compose YAML")
		}
		if document["services"] == nil {
			t.Fatal("missing services")
		}
		if name == "compose.yaml" {
			services := document["services"].(map[string]any)
			for _, service := range []string{"postgres", "migrate", "bot"} {
				if services[service] == nil {
					t.Fatal("missing service", service)
				}
			}
			postgres := services["postgres"].(map[string]any)
			if postgres["image"] != "postgres:18.0-bookworm" {
				t.Fatal("unfixed database image")
			}
			volumes := postgres["volumes"].([]any)
			if volumes[0] != "postgres_data:/var/lib/postgresql" {
				t.Fatal("incorrect PostgreSQL 18 persistence path")
			}
			bot := services["bot"].(map[string]any)
			if bot["read_only"] != true {
				t.Fatal("mutable bot container")
			}
			deps := bot["depends_on"].(map[string]any)
			if deps["migrate"].(map[string]any)["condition"] != "service_completed_successfully" {
				t.Fatal("run does not wait for explicit migrations")
			}
		}
	}
}
