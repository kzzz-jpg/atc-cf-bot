package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/render"
)

func TestParseButtonAcceptsSolveKind(t *testing.T) {
	id := "0192f0ab-cdef-7000-8000-0123456789ab"
	kind, pub, e := ParseButton("v1:solved:" + id)
	if e != nil || kind != "solved" || pub != id {
		t.Fatalf("solved button rejected: kind=%s pub=%s error=%v", kind, pub, e)
	}
	if _, _, e = ParseButton("v1:backlog:pick-30"); e == nil {
		t.Fatal("backlog component must not parse as a publication button")
	}
}

func TestParseBacklogID(t *testing.T) {
	kind, page, days, e := parseBacklogID("v1:backlog:pick-30")
	if e != nil || kind != "pick" || page != 0 || days != 30 {
		t.Fatalf("pick parsed kind=%s page=%d days=%d error=%v", kind, page, days, e)
	}
	kind, page, days, e = parseBacklogID("v1:backlog:pg-2-366")
	if e != nil || kind != "pg" || page != 2 || days != 366 {
		t.Fatalf("page parsed kind=%s page=%d days=%d error=%v", kind, page, days, e)
	}
	for _, bad := range []string{"v1:backlog:pg-0-30", "v1:backlog:pg-1-400", "v1:backlog:pick-0", "v1:backlog:other-30", "v1:backlog:pg-1", "v1:backlog:pg-x-30"} {
		if _, _, _, e = parseBacklogID(bad); e == nil {
			t.Fatalf("invalid backlog id accepted: %q", bad)
		}
	}
}

func interaction() *discordgo.Interaction {
	return &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}
}

func TestReplyRichEphemeralEditsOriginal(t *testing.T) {
	embeds := []render.Embed{{Title: "解題統計", Description: "🟩", Color: progressColor}}
	components := []render.Row{{Type: 1, Components: []render.Button{{Type: 3, CustomID: "v1:backlog:pick-30", Placeholder: "選取", Options: []render.SelectOption{{Label: "a", Value: "v"}}}}}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var p struct {
			Content    string         `json:"content"`
			Flags      int            `json:"flags"`
			Embeds     []render.Embed `json:"embeds"`
			Components []render.Row   `json:"components"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			return
		}
		if r.Method != http.MethodPatch || p.Flags != 64 || len(p.Embeds) != 1 || p.Embeds[0].Title != "解題統計" || len(p.Components) != 1 || p.Components[0].Components[0].CustomID != "v1:backlog:pick-30" {
			t.Errorf("unexpected ephemeral rich reply: %s %+v", r.Method, p)
		}
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	if err := a.ReplyRich(context.Background(), interaction(), RichReply{Embeds: embeds, Components: components}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("ephemeral reply made %d requests", calls)
	}
}

func TestReplyRichPublicPostsFollowup(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		var p struct {
			Content string         `json:"content"`
			Flags   int            `json:"flags"`
			Embeds  []render.Embed `json:"embeds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			return
		}
		switch len(requests) {
		case 1:
			if r.Method != http.MethodPatch || p.Content != "已在頻道回覆。" || p.Flags != 64 || len(p.Embeds) != 0 {
				t.Errorf("original not resolved with a private note: %+v", p)
			}
		case 2:
			if r.Method != http.MethodPost || p.Flags != 0 || len(p.Embeds) != 1 {
				t.Errorf("public followup malformed: %s flags=%d embeds=%d", r.Method, p.Flags, len(p.Embeds))
			}
		}
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	err := a.ReplyRich(context.Background(), interaction(), RichReply{Content: "公開統計", Embeds: []render.Embed{{Title: "解題統計", Description: "🟩"}}, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[1] != "POST /webhooks/987654321/interaction" {
		t.Fatalf("unexpected request sequence %v", requests)
	}
}

func TestReplyRichRejectsOversizedPayload(t *testing.T) {
	a, _ := New("secret", nil)
	long := make([]render.Embed, 11)
	if err := a.ReplyRich(context.Background(), interaction(), RichReply{Embeds: long}); err == nil {
		t.Fatal("oversized embed list accepted")
	}
	if err := a.ReplyRich(context.Background(), interaction(), RichReply{Embeds: []render.Embed{{Description: strings.Repeat("中", 5000)}}}); err == nil {
		t.Fatal("oversized description accepted")
	}
	rows := make([]render.Row, 6)
	if err := a.ReplyRich(context.Background(), interaction(), RichReply{Components: rows}); err == nil {
		t.Fatal("oversized component list accepted")
	}
}
