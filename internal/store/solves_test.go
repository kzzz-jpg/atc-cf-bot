package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
)

// publishedProblem reserves a catalog candidate for a publication, stores the
// statement and translation, completes the publication's message send and
// returns the publication and its message identity.
func publishedProblem(t *testing.T, s *Store, j JobLease, p Publication, ref domain.ProblemRef, threadID, messageID string) {
	t.Helper()
	ctx := context.Background()
	if e := s.SaveCatalog(ctx, domain.CatalogSnapshot{Platform: ref.Platform, SourceHash: "hash-" + ref.ExternalID, Entries: []domain.ProblemRef{ref}}); e != nil {
		t.Fatal(e)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT id FROM problems WHERE external_id=$1", ref.ExternalID).Scan(&ref.ID)
	_ = s.Pool.QueryRow(ctx, "SELECT snapshot_id FROM catalog_heads WHERE platform=$1", ref.Platform).Scan(&ref.CatalogSnapshotID)
	if e := s.Reserve(ctx, j, p, ref, 90); e != nil {
		t.Fatal(e)
	}
	st := domain.Statement{SchemaVersion: "1", ParserVersion: "html-v1", Language: "en", Title: "A", URL: ref.URL, Blocks: []domain.Block{{ID: "b1", Kind: "statement", Text: "text"}}, Samples: []domain.Sample{{Input: "1\n", Output: "2\n"}}}
	sid, e := s.SaveStatement(ctx, ref.ID, st, "raw")
	if e != nil {
		t.Fatal(e)
	}
	key := domain.Hash([]byte("key-" + ref.ExternalID))
	owner := domain.NewID()
	if e = s.BeginGeneration(ctx, key, sid, "translation", owner, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if e = s.EndGeneration(ctx, key, owner, json.RawMessage(`{"blocks":{"b1":"中文"}}`), nil); e != nil {
		t.Fatal(e)
	}
	if e = s.ReadyPublication(ctx, j, p.ID, sid, key); e != nil {
		t.Fatal(e)
	}
	payload := render.NewPayload("題目內容")
	payload.ChannelID = threadID
	payload.Components = []render.Row{{Type: 1, Components: []render.Button{
		{Type: 2, Style: 5, Label: "前往題目", URL: ref.URL},
		{Type: 2, Style: 1, Label: "給我提示", CustomID: "v1:hint:" + p.ID},
		{Type: 2, Style: 2, Label: "給我題解", CustomID: "v1:solution:" + p.ID},
		{Type: 2, Style: 3, Label: "回報解題 ✅", CustomID: "v1:solved:" + p.ID},
	}}}
	if e = s.PrepareOperations(ctx, j, p.ID, "message", []render.Payload{payload}, ""); e != nil {
		t.Fatal(e)
	}
	ops, e := s.Operations(ctx, j.GuildID, j.DailyID, p.ID, "message")
	if e != nil || len(ops) != 1 {
		t.Fatalf("message ops %v %v", ops, e)
	}
	o := ops[0]
	if e = s.MarkSending(ctx, j, &o); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordResult(ctx, j, o, DiscordResult{ID: messageID}); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresUserSolves(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := "123456789012345678"
	user := "111111111111111111"
	setupGuild(t, s, g)
	if _, e := s.Accept(ctx, AcceptRequest{GuildID: g}); e != nil {
		t.Fatal(e)
	}
	j, e := s.ClaimJob(ctx, domain.NewID())
	if e != nil {
		t.Fatal(e)
	}
	pubs, e := s.Publications(ctx, j)
	if e != nil || len(pubs) != 2 {
		t.Fatalf("publications %v %v", pubs, e)
	}
	atCoderDifficulty := 400
	cfDifficulty := 1000
	atcoderRef := domain.ProblemRef{Platform: domain.AtCoder, ExternalID: "abc001_1", URL: "https://atcoder.jp/contests/abc001/tasks/abc001_1", ContestID: "abc001", Difficulty: &atCoderDifficulty, ContestEnd: time.Now().Add(-24 * time.Hour), Finished: true, Kind: "programming", Metadata: json.RawMessage(`{}`)}
	cfRef := domain.ProblemRef{Platform: domain.Codeforces, ExternalID: "1234A", URL: "https://codeforces.com/contest/1234/problem/A", ContestID: "1234", Difficulty: &cfDifficulty, ContestEnd: time.Now().Add(-24 * time.Hour), Finished: true, Kind: "programming", Metadata: json.RawMessage(`{}`)}
	forum := render.NewPayload("每日題目")
	forum.ChannelID = "123456789123456789"
	forum.Title = "每日題目"
	if e = s.PrepareOperations(ctx, j, "", "forum", []render.Payload{forum}, ""); e != nil {
		t.Fatal(e)
	}
	ops, e := s.Operations(ctx, g, j.DailyID, "", "forum")
	if e != nil || len(ops) != 1 {
		t.Fatalf("forum ops %v %v", ops, e)
	}
	o := ops[0]
	if e = s.MarkSending(ctx, j, &o); e != nil {
		t.Fatal(e)
	}
	threadID, starterID := "444444444444444444", "555555555555555555"
	if e = s.RecordResult(ctx, j, o, DiscordResult{ID: threadID, ThreadID: threadID, StarterID: starterID}); e != nil {
		t.Fatal(e)
	}
	publishedProblem(t, s, j, pubs[0], atcoderRef, threadID, "666666666666666666")
	publishedProblem(t, s, j, pubs[1], cfRef, threadID, "777777777777777777")

	// The solve button only verifies against its own publication's message.
	item, e := s.ButtonSolve(ctx, g, threadID, "666666666666666666", pubs[0].ID)
	if e != nil || item.Platform != domain.AtCoder || item.ExternalID != "abc001_1" || item.Date == "" {
		t.Fatalf("button solve %+v %v", item, e)
	}
	if _, e = s.ButtonSolve(ctx, g, threadID, "666666666666666666", pubs[1].ID); e == nil {
		t.Fatal("mismatched publication and message accepted")
	}
	if _, e = s.ButtonSolve(ctx, g, threadID, "000000000000000000", pubs[0].ID); e == nil {
		t.Fatal("unknown message accepted")
	}

	cfg, e := s.Config(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	now, e := s.Now(ctx)
	if e != nil {
		t.Fatal(e)
	}
	today := domain.CycleAt(now, cfg.Settings).Date

	// Toggling flips state and reports what happened.
	added, e := s.ToggleSolve(ctx, g, user, pubs[0].ID, today, "button")
	if e != nil || !added {
		t.Fatalf("first toggle added=%v error=%v", added, e)
	}
	added, e = s.ToggleSolve(ctx, g, user, pubs[0].ID, today, "button")
	if e != nil || added {
		t.Fatalf("second toggle added=%v error=%v", added, e)
	}
	if _, e = s.ToggleSolve(ctx, "223456789012345678", user, pubs[0].ID, today, "button"); e == nil {
		t.Fatal("cross-guild toggle accepted")
	}
	// Backlog marking skips problems that already have a solve.
	count, e := s.MarkSolves(ctx, g, user, []string{pubs[0].ID, pubs[1].ID}, today, "backlog")
	if e != nil || count != 2 {
		t.Fatalf("mark solves count=%d error=%v", count, e)
	}
	count, e = s.MarkSolves(ctx, g, user, []string{pubs[0].ID}, today, "backlog")
	if e != nil || count != 0 {
		t.Fatalf("duplicate solve count=%d error=%v", count, e)
	}

	stats, e := s.SolveStats(ctx, g, user)
	if e != nil || stats.Total != 2 || stats.AtCoder != 1 || stats.Codeforces != 1 || stats.Days[today] != 2 {
		t.Fatalf("stats %+v error=%v", stats, e)
	}
	other := "222222222222222222"
	stats, e = s.SolveStats(ctx, g, other)
	if e != nil || stats.Total != 0 || len(stats.Days) != 0 {
		t.Fatalf("empty stats %+v error=%v", stats, e)
	}

	backlog, e := s.Backlog(ctx, g, user, today, 50)
	if e != nil || len(backlog) != 0 {
		t.Fatalf("solved user backlog %v %v", backlog, e)
	}
	backlog, e = s.Backlog(ctx, g, other, today, 50)
	if e != nil || len(backlog) != 2 {
		t.Fatalf("unsolved backlog %v %v", backlog, e)
	}
	// Slot order puts the AtCoder problem first within the same date.
	if backlog[0].Platform != domain.AtCoder || backlog[0].ThreadID != threadID || backlog[0].MessageID == "" || backlog[0].Date == "" || backlog[0].PublicationID == "" {
		t.Fatalf("backlog item incomplete %+v", backlog[0])
	}

	board, e := s.Leaderboard(ctx, g, today)
	if e != nil || len(board) != 1 || board[0].UserID != user || board[0].Count != 2 {
		t.Fatalf("leaderboard %v %v", board, e)
	}
}
