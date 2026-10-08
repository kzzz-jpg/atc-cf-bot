package store

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL 18 database")
	}
	ctx := context.Background()
	lock, e := AcquireSingleton(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	if e = Migrate(ctx, dsn); e != nil {
		lock.Close()
		t.Fatal(e)
	}
	lock.Close()
	c := config.Default()
	c.Database.URL = dsn
	c.Database.AllowInsecure = true
	s, e := Open(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CheckSchema(ctx); e != nil {
		t.Fatal(e)
	}
	var version string
	if e = s.Pool.QueryRow(ctx, "SHOW server_version").Scan(&version); e != nil || version[:2] != "18" {
		t.Fatal("requires PostgreSQL 18")
	}
	if _, e = s.Pool.Exec(ctx, "TRUNCATE guild_configs,catalog_snapshots,problems,statement_assets CASCADE"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func setupGuild(t *testing.T, s *Store, g string) {
	t.Helper()
	ctx := context.Background()
	if e := s.EnsureGuild(ctx, g); e != nil {
		t.Fatal(e)
	}
	cfg, e := s.Config(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Settings.ForumChannelID = "123456789123456789"
	cfg.Settings.GenerationCooldown = 0
	cfg.Settings.AtCoderMin = -20
	if _, e = s.UpdateConfig(ctx, g, cfg.Version, cfg.Settings); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresAdmissionLeasesRecovery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	req := AcceptRequest{GuildID: g, UserID: g, InteractionID: "333333333333333333", Manual: true, GuildLimit: 5, GlobalLimit: 100}
	manual, e := s.Accept(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	daily, e := s.Accept(ctx, AcceptRequest{GuildID: g})
	if e != nil {
		t.Fatal(e)
	}
	if manual.DailyID != daily.DailyID {
		t.Fatal("manual did not share daily")
	}
	if _, e = s.Pool.Exec(ctx, "DELETE FROM interaction_receipts"); e != nil {
		t.Fatal(e)
	}
	duplicate, e := s.Accept(ctx, req)
	if e != nil || duplicate.ID != manual.ID {
		t.Fatal("receipt cleanup broke dedup")
	}
	var n int
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM problem_publications WHERE daily_id=$1", daily.DailyID).Scan(&n)
	if n != 3 {
		t.Fatalf("quota changed: %d", n)
	}
	var leases []JobLease
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := s.ClaimJob(ctx, domain.NewID())
			if e == nil {
				mu.Lock()
				leases = append(leases, j)
				mu.Unlock()
			} else if e != pgx.ErrNoRows {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if len(leases) != 1 {
		t.Fatalf("same guild claimed %d", len(leases))
	}
	j := leases[0]
	p := render.NewPayload("frozen")
	p.ChannelID = "123456789123456789"
	p.Title = "test"
	if e = s.PrepareOperations(ctx, j, "", "forum", []render.Payload{p}, ""); e != nil {
		t.Fatal(e)
	}
	ops, e := s.Operations(ctx, g, j.DailyID, "", "forum")
	if e != nil || len(ops) != 1 {
		t.Fatalf("ops %v %v", ops, e)
	}
	o := ops[0]
	if e = s.MarkSending(ctx, j, &o); e != nil {
		t.Fatal(e)
	}
	if e = s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	uncertain, e := s.Uncertain(ctx)
	if e != nil || len(uncertain) != 1 {
		t.Fatalf("lost send not recovered %v", e)
	}
	if _, e = s.ClaimJob(ctx, domain.NewID()); e != pgx.ErrNoRows {
		t.Fatal("uncertain guild accepted job")
	}
	if e = s.RecordResult(ctx, j, o, DiscordResult{ID: "444444444444444444"}); e == nil {
		t.Fatal("old epoch wrote")
	}
	r := DiscordResult{ID: "444444444444444444", ThreadID: "444444444444444444", StarterID: "444444444444444444"}
	if e = s.Decide(ctx, uncertain[0], r, "adopt", "test", "verified payload"); e != nil {
		t.Fatal(e)
	}
	resumed, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	if resumed.Epoch <= j.Epoch {
		t.Fatal("epoch did not advance")
	}
	if e = s.CheckLease(ctx, j); e == nil {
		t.Fatal("stale owner authorized")
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE guild_runtime SET lease_until=clock_timestamp()-interval '1 second' WHERE guild_id=$1", g); e != nil {
		t.Fatal(e)
	}
	if e = s.Heartbeat(ctx, resumed); e == nil {
		t.Fatal("expired lease resurrected")
	}
}
func TestPostgresConstraintsAndImmutableContent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	other := "223456789012345678"
	setupGuild(t, s, other)
	job, e := s.Accept(ctx, AcceptRequest{GuildID: g})
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	pubs, e := s.Publications(ctx, j)
	if e != nil {
		t.Fatal(e)
	}
	p := pubs[0]
	difficulty := -10
	ref := domain.ProblemRef{Platform: domain.AtCoder, ExternalID: "abc001_1", URL: "https://atcoder.jp/contests/abc001/tasks/abc001_1", ContestID: "abc001", Difficulty: &difficulty, ContestEnd: time.Now().Add(-24 * time.Hour), Finished: true, Kind: "programming", Metadata: json.RawMessage(`{}`)}
	if e = s.SaveCatalog(ctx, domain.CatalogSnapshot{Platform: domain.AtCoder, SourceHash: "hash", Entries: []domain.ProblemRef{ref}}); e != nil {
		t.Fatal(e)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT id FROM problems WHERE external_id=$1", ref.ExternalID).Scan(&ref.ID)
	_ = s.Pool.QueryRow(ctx, "SELECT snapshot_id FROM catalog_heads WHERE platform=$1", ref.Platform).Scan(&ref.CatalogSnapshotID)
	if e = s.Reserve(ctx, j, p, ref, 90); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "INSERT INTO problem_reservations(guild_id,problem_id,publication_id) VALUES($1,$2,$3)", other, ref.ID, p.ID); e == nil {
		t.Fatal("cross-guild reservation accepted")
	}
	st := domain.Statement{SchemaVersion: "1", ParserVersion: "html-v1", Language: "en", Title: "A", URL: ref.URL, Blocks: []domain.Block{{ID: "b1", Kind: "statement", Text: "text"}}, Samples: []domain.Sample{{Input: "1\n", Output: "2\n"}}}
	sid, e := s.SaveStatement(ctx, ref.ID, st, "raw")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE statement_versions SET raw_html='changed' WHERE id=$1", sid); e == nil {
		t.Fatal("statement changed")
	}
	key := domain.Hash([]byte("key"))
	owner := domain.NewID()
	if e = s.BeginGeneration(ctx, key, sid, "translation", owner, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if e = s.EndGeneration(ctx, key, owner, json.RawMessage(`{"blocks":{"b1":"中文"}}`), nil); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE generation_entries SET result='{}' WHERE cache_key=$1", key); e == nil {
		t.Fatal("ready cache overwritten")
	}
	if e = s.ReadyPublication(ctx, j, p.ID, sid, key); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE problem_publications SET status='completed' WHERE id=$1", p.ID); e == nil {
		t.Fatal("incomplete publication completed")
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE posting_jobs SET guild_id=$2 WHERE id=$1", job.ID, other); e == nil {
		t.Fatal("cross-guild job accepted")
	}
}
func TestPostgresCancelledTransactionAndSingleton(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	lock, e := AcquireSingleton(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	if duplicate, e := AcquireSingleton(ctx, os.Getenv("TEST_DATABASE_URL")); e == nil {
		duplicate.Close()
		t.Fatal("second singleton accepted")
	}
	lock.Close()
	short, c := context.WithTimeout(ctx, 10*time.Millisecond)
	defer c()
	e = s.transaction(short, func(tx pgx.Tx) error { _, e := tx.Exec(short, "SELECT pg_sleep(1)"); return e })
	if e == nil {
		t.Fatal("cancel failed")
	}
	for i := 0; i < 20; i++ {
		if e = s.Pool.Ping(ctx); e != nil {
			t.Fatal("pool leaked transaction", e)
		}
	}
}
func TestPostgresSnapshotQueueAndExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	cfg, e := s.Config(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	req := AcceptRequest{GuildID: g, Manual: true, GuildLimit: 2, GlobalLimit: 2}
	for _, id := range []string{"311111111111111111", "322222222222222222"} {
		req.InteractionID = id
		if _, e = s.Accept(ctx, req); e != nil {
			t.Fatal(e)
		}
	}
	req.InteractionID = "333333333333333333"
	if _, e = s.Accept(ctx, req); e == nil {
		t.Fatal("queue cap ignored")
	}
	var count int
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM problem_publications WHERE kind='manual'").Scan(&count)
	if count != 2 {
		t.Fatal("rejected request allocated slot")
	}
	cfg.Settings.DailyCount = 5
	if _, e = s.UpdateConfig(ctx, g, cfg.Version, cfg.Settings); e != nil {
		t.Fatal(e)
	}
	d, e := s.Today(ctx, g)
	if e != nil || d.Target != 2 {
		t.Fatal("daily snapshot changed")
	}
	if _, e = s.UpdateConfig(ctx, g, cfg.Version, cfg.Settings); e == nil {
		t.Fatal("lost update accepted")
	}
	j, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE guild_runtime SET lease_until=clock_timestamp()-interval '1 second' WHERE guild_id=$1", g); e != nil {
		t.Fatal(e)
	}
	if e = s.RecoverExpired(ctx); e != nil {
		t.Fatal(e)
	}
	next, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil || next.ID != j.ID || next.Epoch <= j.Epoch {
		t.Fatal("expired owner not recovered", e)
	}
	if e = s.CheckLease(ctx, j); e == nil {
		t.Fatal("old owner wrote after reclaim")
	}
}
func TestPostgresAtomicCatalogAndCrossGuildClaims(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	diff := 500
	ref := domain.ProblemRef{Platform: domain.AtCoder, ExternalID: "abc001_1", URL: "https://atcoder.jp/contests/abc001/tasks/abc001_1", ContestID: "abc001", Difficulty: &diff, ContestEnd: time.Now().Add(-time.Hour), Finished: true, Kind: "programming", Metadata: json.RawMessage(`{}`)}
	if e := s.SaveCatalog(ctx, domain.CatalogSnapshot{Platform: domain.AtCoder, SourceHash: "old", Entries: []domain.ProblemRef{ref}}); e != nil {
		t.Fatal(e)
	}
	var before string
	_ = s.Pool.QueryRow(ctx, "SELECT snapshot_id FROM catalog_heads WHERE platform='atcoder'").Scan(&before)
	broken := ref
	broken.ExternalID = ""
	if e := s.SaveCatalog(ctx, domain.CatalogSnapshot{Platform: domain.AtCoder, SourceHash: "broken", Entries: []domain.ProblemRef{ref, broken}}); e == nil {
		t.Fatal("broken batch accepted")
	}
	var after string
	_ = s.Pool.QueryRow(ctx, "SELECT snapshot_id FROM catalog_heads WHERE platform='atcoder'").Scan(&after)
	if before != after {
		t.Fatal("partial catalog became head")
	}
	for _, g := range []string{"123456789012345678", "223456789012345678"} {
		setupGuild(t, s, g)
		if _, e := s.Accept(ctx, AcceptRequest{GuildID: g}); e != nil {
			t.Fatal(e)
		}
	}
	a, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil || a.GuildID == b.GuildID {
		t.Fatal("different guilds cannot execute concurrently", e)
	}
}
func TestPostgresPendingTimezoneAndSchemaGuard(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	cfg, e := s.Config(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Settings.Timezone = "UTC"
	pending, e := s.UpdateConfig(ctx, g, cfg.Version, cfg.Settings)
	if e != nil || !pending {
		t.Fatal("timezone not deferred", e)
	}
	current, e := s.Config(ctx, g)
	if e != nil || current.Settings.Timezone != "Asia/Taipei" {
		t.Fatal("timezone changed mid-day")
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE guild_configs SET pending_effective_at=clock_timestamp()-interval '1 second' WHERE guild_id=$1", g); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyPending(ctx, g); e != nil {
		t.Fatal(e)
	}
	current, e = s.Config(ctx, g)
	if e != nil || current.Settings.Timezone != "UTC" {
		t.Fatal("pending timezone did not apply")
	}
	if _, e = s.Pool.Exec(ctx, "ALTER TABLE outbound_operations DISABLE TRIGGER immutable_payload"); e != nil {
		t.Fatal(e)
	}
	if s.CheckSchema(ctx) == nil {
		t.Fatal("broken actual schema accepted")
	}
	if _, e = s.Pool.Exec(ctx, "ALTER TABLE outbound_operations ENABLE TRIGGER immutable_payload"); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresFailedCandidateNotSelectedAgain(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	if _, e := s.Accept(ctx, AcceptRequest{GuildID: g}); e != nil {
		t.Fatal(e)
	}
	j, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	pubs, e := s.Publications(ctx, j)
	if e != nil {
		t.Fatal(e)
	}
	p := pubs[0]
	difficulty := 500
	ref := domain.ProblemRef{Platform: domain.AtCoder, ExternalID: "abc001_1", URL: "https://atcoder.jp/contests/abc001/tasks/a", ContestID: "abc001", Difficulty: &difficulty, ContestEnd: time.Now().Add(-time.Hour), Finished: true, Kind: "programming", Metadata: json.RawMessage(`{}`)}
	if e = s.SaveCatalog(ctx, domain.CatalogSnapshot{Platform: domain.AtCoder, SourceHash: "test", Entries: []domain.ProblemRef{ref}}); e != nil {
		t.Fatal(e)
	}
	candidates, e := s.Candidates(ctx, j, p, 90)
	if e != nil || len(candidates) != 1 {
		t.Fatal("missing candidate", e)
	}
	if e = s.Reserve(ctx, j, p, candidates[0], 90); e != nil {
		t.Fatal(e)
	}
	if e = s.RejectCandidate(ctx, j, p, false); e != nil {
		t.Fatal(e)
	}
	candidates, e = s.Candidates(ctx, j, p, 90)
	if e != nil || len(candidates) != 0 {
		t.Fatal("same failed candidate reselected", e)
	}
	if e = s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	j, e = s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	pubs, e = s.Publications(ctx, j)
	if e != nil || pubs[0].Attempts != 1 {
		t.Fatal("candidate attempts reset after restart", e)
	}
}
func readyTestPublication(t *testing.T, s *Store, j JobLease) Publication {
	t.Helper()
	ctx := context.Background()
	pubs, e := s.Publications(ctx, j)
	if e != nil {
		t.Fatal(e)
	}
	p := pubs[0]
	difficulty := 500
	ref := domain.ProblemRef{Platform: domain.AtCoder, ExternalID: "abc001_1", URL: "https://atcoder.jp/contests/abc001/tasks/a", ContestID: "abc001", Difficulty: &difficulty, ContestEnd: time.Now().Add(-time.Hour), Finished: true, Kind: "programming", Metadata: json.RawMessage(`{}`)}
	if e = s.SaveCatalog(ctx, domain.CatalogSnapshot{Platform: domain.AtCoder, SourceHash: "test", Entries: []domain.ProblemRef{ref}}); e != nil {
		t.Fatal(e)
	}
	candidates, e := s.Candidates(ctx, j, p, 90)
	if e != nil || len(candidates) != 1 {
		t.Fatal(e)
	}
	ref = candidates[0]
	if e = s.Reserve(ctx, j, p, ref, 90); e != nil {
		t.Fatal(e)
	}
	statement := domain.Statement{SchemaVersion: "1", ParserVersion: "html-v1", Language: "en", Title: "A", URL: ref.URL, Blocks: []domain.Block{{ID: "b1", Kind: "statement", Text: "text"}, {ID: "b2", Kind: "input", Text: "input"}, {ID: "b3", Kind: "output", Text: "output"}}, Samples: []domain.Sample{{Input: "1\n", Output: "2\n"}}}
	sid, e := s.SaveStatement(ctx, ref.ID, statement, "raw")
	if e != nil {
		t.Fatal(e)
	}
	key := domain.Hash([]byte(sid))
	owner := domain.NewID()
	if e = s.BeginGeneration(ctx, key, sid, "translation", owner, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if e = s.EndGeneration(ctx, key, owner, json.RawMessage(`{"blocks":{"b1":"中文","b2":"輸入","b3":"輸出"}}`), nil); e != nil {
		t.Fatal(e)
	}
	if e = s.ReadyPublication(ctx, j, p.ID, sid, key); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPostgresConfirmedSegmentsSurviveLostNextResult(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	if _, e := s.Accept(ctx, AcceptRequest{GuildID: g}); e != nil {
		t.Fatal(e)
	}
	j, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	p := readyTestPublication(t, s, j)
	p1 := render.NewPayload("first frozen section")
	p1.ChannelID = "223456789012345678"
	p2 := render.NewPayload("second frozen section")
	p2.ChannelID = p1.ChannelID
	if e = s.PrepareOperations(ctx, j, p.ID, "message", []render.Payload{p1, p2}, ""); e != nil {
		t.Fatal(e)
	}
	ops, e := s.Operations(ctx, g, j.DailyID, p.ID, "message")
	if e != nil || len(ops) != 2 {
		t.Fatal(e)
	}
	if e = s.MarkSending(ctx, j, &ops[0]); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordResult(ctx, j, ops[0], DiscordResult{ID: "444444444444444444"}); e != nil {
		t.Fatal(e)
	}
	if e = s.MarkSending(ctx, j, &ops[1]); e != nil {
		t.Fatal(e)
	}
	if e = s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	after, e := s.Operations(ctx, g, j.DailyID, p.ID, "message")
	if e != nil || after[0].Status != "confirmed" || after[0].DiscordID != "444444444444444444" || after[1].Status != "ambiguous" || after[1].Payload.Content != p2.Content {
		t.Fatal("restart lost progress", e)
	}
	if e = s.MarkSending(ctx, j, &ops[0]); e == nil {
		t.Fatal("old owner resent confirmed segment")
	}
}

func TestPostgresExhaustedRetryReleasesPublicationOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	if _, e := s.Accept(ctx, AcceptRequest{GuildID: g}); e != nil {
		t.Fatal(e)
	}
	j, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE posting_jobs SET auto_retry_count=2 WHERE id=$1", j.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE guild_runtime SET resume_job_id=$2 WHERE guild_id=$1", g, j.ID); e != nil {
		t.Fatal(e)
	}
	next, e := s.Accept(ctx, AcceptRequest{GuildID: g, UserID: g, InteractionID: "333333333333333333", Manual: true, GuildLimit: 5, GlobalLimit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishJob(ctx, j, "retry_wait", "transient", true); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = s.Pool.QueryRow(ctx, "SELECT status FROM posting_jobs WHERE id=$1", j.ID).Scan(&status); e != nil || status != "failed" {
		t.Fatal("retry limit not applied", e, status)
	}
	claimed, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil || claimed.ID != next.ID {
		t.Fatal("failed job retained publication order", e)
	}
}

func TestPostgresSchedulerUsesFrozenDailyTime(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	cfg, e := s.Config(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	now, e := s.Now(ctx)
	if e != nil {
		t.Fatal(e)
	}
	loc, e := time.LoadLocation(cfg.Settings.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	if now.In(loc).Format("15:04") >= "23:59" {
		t.Skip("last minute of local day has no later whole-minute schedule")
	}
	cfg.Settings.PublishTime = "23:59"
	if _, e = s.UpdateConfig(ctx, g, cfg.Version, cfg.Settings); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Accept(ctx, AcceptRequest{GuildID: g, Scheduled: true}); e != ErrNotDue {
		t.Fatal("accepted schedule before DB due time", e)
	}
	var count int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM daily_publications WHERE guild_id=$1", g).Scan(&count); e != nil || count != 0 {
		t.Fatal("not-due check created a daily snapshot", e)
	}
	cfg, e = s.Config(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Settings.PublishTime = "00:00"
	if _, e = s.UpdateConfig(ctx, g, cfg.Version, cfg.Settings); e != nil {
		t.Fatal(e)
	}
	manual, e := s.Accept(ctx, AcceptRequest{GuildID: g, UserID: g, InteractionID: "333333333333333333", Manual: true, GuildLimit: 5, GlobalLimit: 100})
	if e != nil {
		t.Fatal(e)
	}
	cfg, e = s.Config(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Settings.PublishTime = "23:59"
	if _, e = s.UpdateConfig(ctx, g, cfg.Version, cfg.Settings); e != nil {
		t.Fatal(e)
	}
	scheduled, e := s.Accept(ctx, AcceptRequest{GuildID: g, Scheduled: true})
	if e != nil || scheduled.DailyID != manual.DailyID {
		t.Fatal("latest settings postponed frozen daily schedule", e)
	}
	duplicate, e := s.Accept(ctx, AcceptRequest{GuildID: g, Scheduled: true})
	if e != nil || duplicate.ID != scheduled.ID {
		t.Fatal("repeated schedule duplicated job", e)
	}
}

func TestPostgresGracefulInterruptionResumesFrozenSegments(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	setupGuild(t, s, g)
	if _, e := s.Accept(ctx, AcceptRequest{GuildID: g}); e != nil {
		t.Fatal(e)
	}
	j, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	p := readyTestPublication(t, s, j)
	first := render.NewPayload("first frozen section")
	first.ChannelID = "223456789012345678"
	second := render.NewPayload("remaining frozen section")
	second.ChannelID = first.ChannelID
	if e = s.PrepareOperations(ctx, j, p.ID, "message", []render.Payload{first, second}, ""); e != nil {
		t.Fatal(e)
	}
	ops, e := s.Operations(ctx, g, j.DailyID, p.ID, "message")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.MarkSending(ctx, j, &ops[0]); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordResult(ctx, j, ops[0], DiscordResult{ID: "444444444444444444"}); e != nil {
		t.Fatal(e)
	}
	// The old execution deadline must not remain on a resumable queued job.
	if _, e = s.Pool.Exec(ctx, "UPDATE posting_jobs SET deadline_at=clock_timestamp()-interval '1 second' WHERE id=$1", j.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishJob(ctx, j, "queued", "interrupted", false); e != nil {
		t.Fatal(e)
	}
	if e = s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	resumed, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil || resumed.ID != j.ID || resumed.Epoch <= j.Epoch {
		t.Fatal("interrupted job not resumed", e)
	}
	after, e := s.Operations(ctx, g, j.DailyID, p.ID, "message")
	if e != nil || len(after) != 2 || after[0].Status != "confirmed" || after[1].Status != "prepared" || after[1].Payload.Content != second.Content {
		t.Fatal("interrupted progress changed", e)
	}
	var retryCount int
	if e = s.Pool.QueryRow(ctx, "SELECT auto_retry_count FROM posting_jobs WHERE id=$1", j.ID).Scan(&retryCount); e != nil || retryCount != 0 {
		t.Fatal("interruption consumed retry", e)
	}
}

func TestPostgresLostSingletonCancelsApplication(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lock, e := AcquireSingleton(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	stopped := make(chan struct{})
	go func() { lock.Monitor(ctx, cancel); close(stopped) }()
	var killed bool
	if e = s.Pool.QueryRow(ctx, "SELECT pg_terminate_backend($1)", int32(lock.conn.PgConn().PID())).Scan(&killed); e != nil || !killed {
		t.Fatal("could not terminate own lock session", e)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("lost singleton did not cancel application")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("singleton monitor leaked")
	}
	replacement, e := AcquireSingleton(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal("terminated singleton retained lock", e)
	}
	replacement.Close()
}

func TestPostgresFailedMigrationKeepsVersionAndExistingData(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	schema := "migration_failure_" + strings.ReplaceAll(domain.NewID(), "-", "")
	ident := pgx.Identifier{schema}.Sanitize()
	if _, e := s.Pool.Exec(ctx, "CREATE SCHEMA "+ident); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _, _ = s.Pool.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE") })
	// Simulate an incompatible existing table that must never be silently dropped.
	if _, e := s.Pool.Exec(ctx, "CREATE TABLE "+ident+".guild_configs (id integer PRIMARY KEY, payload text NOT NULL)"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "INSERT INTO "+ident+".guild_configs VALUES (1,'existing data')"); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn := u.String()
	lock, e := AcquireSingleton(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if e = Migrate(ctx, dsn); e == nil {
		t.Fatal("conflicting schema migration falsely succeeded")
	}
	var payload string
	if e = s.Pool.QueryRow(ctx, "SELECT payload FROM "+ident+".guild_configs WHERE id=1").Scan(&payload); e != nil || payload != "existing data" {
		t.Fatal("failed migration lost existing data", e)
	}
	var version int64
	if e = s.Pool.QueryRow(ctx, "SELECT COALESCE(max(version_id),0) FROM "+ident+".goose_db_version WHERE is_applied").Scan(&version); e != nil || version != 0 {
		t.Fatal("failed migration recorded an applied version", e, version)
	}
	var tables int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema=$1", schema).Scan(&tables); e != nil || tables != 2 {
		t.Fatal("failed migration left partial tables", e, tables)
	}
	cfg := config.Default()
	cfg.Database.URL = dsn
	cfg.Database.AllowInsecure = true
	incomplete, e := Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer incomplete.Close()
	if e = incomplete.CheckSchema(ctx); e == nil {
		t.Fatal("incomplete migrated database accepted for run")
	}
}
