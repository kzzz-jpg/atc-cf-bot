package posting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/generation"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/source"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakePublisher struct {
	mu                sync.Mutex
	sent              map[string]store.DiscordResult
	calls             map[string]int
	failAfterAccept   bool
	failFinalOverview bool
	failedOverviewID  string
}

func (*fakePublisher) CheckForum(context.Context, string, domain.Settings) error { return nil }
func (*fakePublisher) Authorize(context.Context, string, string, string, bool, []string) error {
	return nil
}
func (*fakePublisher) EnsureThread(context.Context, string, string) error { return nil }
func (f *fakePublisher) Execute(_ context.Context, o store.Operation) (store.DiscordResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[o.ID]++
	if f.failFinalOverview && o.Kind == "overview" && strings.Contains(o.Payload.Content, "狀態：completed") {
		f.failFinalOverview = false
		f.failedOverviewID = o.ID
		return store.DiscordResult{}, domain.Fail(domain.Conflict)
	}
	id := strconv.FormatInt(600000000000000000+int64(len(f.sent)), 10)
	r := store.DiscordResult{ID: id}
	if o.Kind == "forum" {
		r.ThreadID = id
		r.StarterID = id
	}
	f.sent[o.ID] = r
	if f.failAfterAccept {
		f.failAfterAccept = false
		return store.DiscordResult{}, domain.Fail(domain.Ambiguous)
	}
	return r, nil
}
func (f *fakePublisher) Reconcile(_ context.Context, _ string, o store.Operation) (store.DiscordResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.sent[o.ID]
	if !ok {
		return r, domain.Fail(domain.Ambiguous)
	}
	return r, nil
}
func TestPostgresEndToEndDailyManualAndRestart(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 18")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	_, e = conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS posting_test")
	_ = conn.Close(ctx)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", "posting_test")
	u.RawQuery = q.Encode()
	dsn = u.String()
	if e = store.Migrate(ctx, dsn); e != nil {
		t.Fatal(e)
	}
	cfg := config.Default()
	cfg.Database.URL = dsn
	cfg.Database.AllowInsecure = true
	db, e := store.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Pool.Exec(ctx, "TRUNCATE guild_configs,catalog_snapshots,problems,statement_assets CASCADE"); e != nil {
		t.Fatal(e)
	}
	guild := "123456789012345678"
	if e = db.EnsureGuild(ctx, guild); e != nil {
		t.Fatal(e)
	}
	gc, e := db.Config(ctx, guild)
	if e != nil {
		t.Fatal(e)
	}
	gc.Settings.ForumChannelID = "223456789012345678"
	gc.Settings.SourceMode = "balanced"
	if _, e = db.UpdateConfig(ctx, guild, gc.Version, gc.Settings); e != nil {
		t.Fatal(e)
	}
	for _, platform := range []domain.Platform{domain.AtCoder, domain.Codeforces} {
		var refs []domain.ProblemRef
		for i := 0; i < 3; i++ {
			diff := 500
			if platform == domain.Codeforces {
				diff = 1000
			}
			refs = append(refs, domain.ProblemRef{Platform: platform, ExternalID: string(platform) + strconv.Itoa(i), URL: map[domain.Platform]string{domain.AtCoder: "https://atcoder.jp/contests/abc001/tasks/a", domain.Codeforces: "https://codeforces.com/problemset/problem/1/A"}[platform], ContestID: "1", Difficulty: &diff, ContestEnd: time.Now().Add(-time.Hour), Finished: true, Kind: "programming", Metadata: json.RawMessage(`{}`)})
		}
		if e = db.SaveCatalog(ctx, domain.CatalogSnapshot{Platform: platform, SourceHash: "test", Entries: refs}); e != nil {
			t.Fatal(e)
		}
	}
	fetcher := source.NewFetcher()
	fetcher.Client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		file := "atcoder.html"
		if r.URL.Hostname() == "codeforces.com" {
			file = "codeforces.html"
		}
		b, e := os.ReadFile("../../testdata/" + file)
		if e != nil {
			return nil, e
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := req.Messages[1].Content
		at := strings.LastIndex(text, "：\n")
		if at < 0 {
			t.Error("translation request malformed")
		}
		text = text[at+len("：\n"):]
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	cfg.LLM.BaseURL = server.URL
	cfg.LLM.Model = "mock"
	client := llm.New(cfg.LLM, 4<<20)
	coordinator := generation.New(ctx, db, 2, 20, 200, 50, 180*time.Second)
	defer func() { cancel(); coordinator.Close() }()
	gen := &generation.Service{Client: client, Coordinator: coordinator, BlockLimit: 16 << 10}
	pub := &fakePublisher{sent: map[string]store.DiscordResult{}, calls: map[string]int{}, failAfterAccept: true}
	service := &Service{Store: db, Publisher: pub, Source: fetcher, Generation: gen, Limits: cfg.Limits, RNG: domain.SecureRNG{}}
	manual, e := db.Accept(ctx, store.AcceptRequest{GuildID: guild, UserID: guild, InteractionID: "333456789012345678", Manual: true, GuildLimit: 5, GlobalLimit: 100})
	if e != nil {
		t.Fatal(e)
	}
	daily, e := db.Accept(ctx, store.AcceptRequest{GuildID: guild})
	if e != nil || daily.DailyID != manual.DailyID {
		t.Fatal("different daily")
	}
	j, e := db.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	if e = service.Run(ctx, j); e != nil {
		t.Fatal(e)
	}
	if e = db.FinishJob(ctx, j, "succeeded", "", false); e != nil {
		t.Fatal(e)
	}
	d, e := db.Daily(ctx, guild, j.DailyID)
	if e != nil || d.Status != "pending" {
		t.Fatalf("manual consumed daily quota: %s %v", d.Status, e)
	}
	j, e = db.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	pub.failFinalOverview = true
	if e = service.Run(ctx, j); e == nil {
		t.Fatal("final overview failure did not interrupt daily job")
	}
	if e = db.FinishJob(ctx, j, "blocked", "conflict", false); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Accept(ctx, store.AcceptRequest{GuildID: guild, AllowResume: true}); e != nil {
		t.Fatal(e)
	}
	j, e = db.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	if e = service.Run(ctx, j); e != nil {
		t.Fatal(e)
	}
	if e = db.FinishJob(ctx, j, "succeeded", "", false); e != nil {
		t.Fatal(e)
	}
	d, e = db.Daily(ctx, guild, j.DailyID)
	if e != nil || d.Status != "completed" {
		t.Fatalf("daily incomplete: %s %v", d.Status, e)
	}
	var count int
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM problem_publications WHERE daily_id=$1 AND status='completed'", d.ID).Scan(&count)
	if count != 3 {
		t.Fatalf("published count %d", count)
	}
	for id, calls := range pub.calls {
		expected := 1
		if id == pub.failedOverviewID {
			expected = 2
			if _, ok := pub.sent[id]; !ok {
				t.Fatal("completed problems prevented the overview retry")
			}
		}
		if calls != expected {
			t.Errorf("operation %s sent %d times", id, calls)
		}
	}
	if e = db.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ClaimJob(ctx, domain.NewID()); e != pgx.ErrNoRows {
		t.Fatal("restart resent complete daily")
	}
	pubs, e := db.Publications(ctx, j)
	if e != nil {
		t.Fatal(e)
	}
	last := pubs[len(pubs)-1]
	ops, e := db.Operations(ctx, guild, d.ID, last.ID, "message")
	if e != nil {
		t.Fatal(e)
	}
	button := ops[len(ops)-1]
	sid, e := db.ButtonStatement(ctx, guild, d.ThreadID, button.DiscordID, last.ID)
	if e != nil || sid == "" {
		t.Fatal("old button broken", e)
	}
	if _, e = db.ButtonStatement(ctx, "999456789012345678", d.ThreadID, button.DiscordID, last.ID); e == nil {
		t.Fatal("cross-guild button authorized")
	}
}
