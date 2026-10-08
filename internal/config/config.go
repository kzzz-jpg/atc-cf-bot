package config

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type LLM struct {
	BaseURL             string   `yaml:"base_url"`
	Model               string   `yaml:"model"`
	TranslationModel    string   `yaml:"translation_model"`
	HintModel           string   `yaml:"hint_model"`
	SolutionModel       string   `yaml:"solution_model"`
	RequestTimeout      int      `yaml:"request_timeout_seconds"`
	OperationTimeout    int      `yaml:"operation_timeout_seconds"`
	MaxRetries          int      `yaml:"max_retries"`
	OutputTokenLimit    int      `yaml:"output_token_limit"`
	TokenLimitParameter string   `yaml:"token_limit_parameter"`
	Temperature         *float64 `yaml:"temperature"`
	MaxConcurrency      int      `yaml:"max_concurrency"`
	MaxQueue            int      `yaml:"max_queue_size"`
	AllowMissingFinish  bool     `yaml:"allow_missing_finish_reason"`
	APIKey              string   `yaml:"-"`
}
type Database struct {
	URL                string `yaml:"-"`
	MaxConnections     int    `yaml:"max_connections"`
	QueryTimeout       int    `yaml:"query_timeout_seconds"`
	AllowInsecure      bool   `yaml:"allow_insecure_local"`
	LockConnectionMode string `yaml:"lock_connection_mode"`
}
type Worker struct {
	Concurrency int `yaml:"public_concurrency"`
	Lease       int `yaml:"lease_seconds"`
	Heartbeat   int `yaml:"heartbeat_seconds"`
}
type Scheduler struct {
	Poll int `yaml:"poll_seconds"`
}
type Discord struct {
	Token       string `yaml:"-"`
	TestGuildID string `yaml:"test_guild_id"`
}
type Limits struct {
	CatalogBytes       int64 `yaml:"catalog_bytes"`
	HTMLBytes          int64 `yaml:"html_bytes"`
	ImageBytes         int64 `yaml:"image_bytes"`
	ImagesTotalBytes   int64 `yaml:"images_total_bytes"`
	ImagePixels        int64 `yaml:"image_pixels"`
	LLMResponseBytes   int64 `yaml:"llm_response_bytes"`
	StatementBytes     int   `yaml:"statement_bytes"`
	TranslationBytes   int   `yaml:"translation_bytes"`
	MessagesPerProblem int   `yaml:"messages_per_problem"`
	Waiters            int   `yaml:"waiters"`
	GuildWaiters       int   `yaml:"guild_waiters"`
	ManualGuildQueue   int   `yaml:"manual_guild_queue"`
	ManualGlobalQueue  int   `yaml:"manual_global_queue"`
}
type Config struct {
	LLM           LLM       `yaml:"llm"`
	Database      Database  `yaml:"database"`
	Worker        Worker    `yaml:"worker"`
	Scheduler     Scheduler `yaml:"scheduler"`
	Discord       Discord   `yaml:"discord"`
	Limits        Limits    `yaml:"limits"`
	HealthAddress string    `yaml:"health_address"`
}

func Default() Config {
	return Config{
		LLM:      LLM{BaseURL: "https://api.openai.com/v1", RequestTimeout: 60, OperationTimeout: 180, MaxRetries: 2, OutputTokenLimit: 8192, TokenLimitParameter: "max_completion_tokens", MaxConcurrency: 2, MaxQueue: 20},
		Database: Database{MaxConnections: 10, QueryTimeout: 5, LockConnectionMode: "direct"}, Worker: Worker{Concurrency: 4, Lease: 90, Heartbeat: 15}, Scheduler: Scheduler{Poll: 30}, HealthAddress: "127.0.0.1:8080",
		Limits: Limits{CatalogBytes: 64 << 20, HTMLBytes: 5 << 20, ImageBytes: 2 << 20, ImagesTotalBytes: 8 << 20, ImagePixels: 16e6, LLMResponseBytes: 4 << 20, StatementBytes: 64 << 10, TranslationBytes: 16 << 10, MessagesPerProblem: 64, Waiters: 200, GuildWaiters: 50, ManualGuildQueue: 5, ManualGlobalQueue: 100},
	}
}
func Load(path string, getenv func(string) (string, bool), databaseOnly bool) (Config, error) {
	c := Default()
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, fmt.Errorf("cannot read configuration")
		}
		if e = decode(b, &c); e != nil {
			return c, e
		}
	}
	// Every non-secret deployment field has a matching uppercase dotted-path env.
	if e := overlay(reflect.ValueOf(&c).Elem(), "", getenv); e != nil {
		return c, e
	}
	c.Database.URL, _ = getenv("DATABASE_URL")
	c.Discord.Token, _ = getenv("DISCORD_BOT_TOKEN")
	c.LLM.APIKey, _ = getenv("LLM_API_KEY")
	return c, c.Validate(databaseOnly)
}
func decode(b []byte, v any) error {
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if e := d.Decode(v); e != nil {
		return fmt.Errorf("invalid YAML configuration")
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return fmt.Errorf("configuration must contain one YAML document")
	}
	return nil
}
func overlay(v reflect.Value, prefix string, env func(string) (string, bool)) error {
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		tag := typ.Field(i).Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		key := strings.ToUpper(tag)
		if prefix != "" {
			key = prefix + "_" + key
		}
		if f.Kind() == reflect.Struct {
			if e := overlay(f, key, env); e != nil {
				return e
			}
			continue
		}
		if raw, ok := env(key); ok {
			if raw == "" {
				return fmt.Errorf("empty environment field %s", key)
			}
			if f.Kind() == reflect.String {
				f.SetString(raw)
			} else if e := yaml.Unmarshal([]byte(raw), f.Addr().Interface()); e != nil {
				return fmt.Errorf("invalid environment field %s", key)
			}
		}
	}
	return nil
}
func (c Config) Validate(dbOnly bool) error {
	bad := func(k string) error { return fmt.Errorf("invalid or missing configuration field %s", k) }
	if c.Database.URL == "" {
		return bad("DATABASE_URL")
	}
	u, e := url.Parse(c.Database.URL)
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return bad("DATABASE_URL")
	}
	if !c.Database.AllowInsecure && u.Query().Get("sslmode") != "verify-full" {
		return bad("database TLS requires sslmode=verify-full")
	}
	if c.Database.MaxConnections < 1 || c.Database.QueryTimeout < 1 {
		return bad("database limits")
	}
	if c.Database.LockConnectionMode != "direct" && c.Database.LockConnectionMode != "session" {
		return bad("database.lock_connection_mode")
	}
	if dbOnly {
		return nil
	}
	if c.Discord.Token == "" {
		return bad("DISCORD_BOT_TOKEN")
	}
	if c.LLM.APIKey == "" {
		return bad("LLM_API_KEY")
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		return bad("llm.model")
	}
	u, e = url.Parse(c.LLM.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") {
		return bad("llm.base_url")
	}
	l := c.LLM
	if l.RequestTimeout < 1 || l.OperationTimeout <= l.RequestTimeout || l.OperationTimeout >= 900 || l.MaxRetries < 0 || l.MaxRetries > 2 || l.OutputTokenLimit < 1 || l.MaxConcurrency < 1 || l.MaxQueue < 0 {
		return bad("llm limits")
	}
	if l.TokenLimitParameter != "max_tokens" && l.TokenLimitParameter != "max_completion_tokens" {
		return bad("llm.token_limit_parameter")
	}
	if l.Temperature != nil && (math.IsNaN(*l.Temperature) || math.IsInf(*l.Temperature, 0) || *l.Temperature < 0 || *l.Temperature > 2) {
		return bad("llm.temperature")
	}
	w := c.Worker
	if w.Concurrency < 1 || w.Heartbeat < 1 || w.Lease <= l.RequestTimeout+w.Heartbeat || w.Heartbeat*2 >= w.Lease {
		return bad("worker lease")
	}
	if c.Scheduler.Poll < 1 {
		return bad("scheduler.poll_seconds")
	}
	v := reflect.ValueOf(c.Limits)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Int() < 1 {
			return bad("limits." + v.Type().Field(i).Tag.Get("yaml"))
		}
	}
	if c.Limits.GuildWaiters > c.Limits.Waiters || c.Limits.ImageBytes > c.Limits.ImagesTotalBytes || c.Limits.TranslationBytes > c.Limits.StatementBytes {
		return bad("resource limits")
	}
	return nil
}
func (l LLM) ModelFor(kind string) string {
	switch kind {
	case "translation":
		if l.TranslationModel != "" {
			return l.TranslationModel
		}
	case "hint":
		if l.HintModel != "" {
			return l.HintModel
		}
	case "solution":
		if l.SolutionModel != "" {
			return l.SolutionModel
		}
	}
	return l.Model
}
func (l LLM) OperationDuration() time.Duration {
	return time.Duration(l.OperationTimeout) * time.Second
}
