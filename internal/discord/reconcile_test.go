package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

func TestReconcileRequiresUniqueBotPayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		author string
		ok     bool
	}{{"unique", 1, "123456789", true}, {"missing", 0, "123456789", false}, {"duplicate", 2, "123456789", false}, {"member", 1, "999999999", false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var messages []*discordgo.Message
				for i := 0; i < tc.count; i++ {
					messages = append(messages, &discordgo.Message{ID: "333333333", Content: "frozen", Author: &discordgo.User{ID: tc.author}})
				}
				_ = json.NewEncoder(w).Encode(messages)
			}))
			defer server.Close()
			a, _ := New("secret", nil)
			a.BaseURL = server.URL
			a.BotID = "123456789"
			p := render.NewPayload("frozen")
			p.ChannelID = "444444444"
			_, e := a.Reconcile(context.Background(), "555555555", store.Operation{Kind: "message", Payload: p})
			if (e == nil) != tc.ok {
				t.Fatalf("unexpected reconcile: %v", e)
			}
		})
	}
}
func TestOverviewConflictNeverEdits(t *testing.T) {
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			patches++
		}
		_ = json.NewEncoder(w).Encode(discordgo.Message{ID: "333333333", Author: &discordgo.User{ID: "123456789"}, Content: "someone changed overview"})
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	a.BotID = "123456789"
	p := render.NewPayload("desired")
	p.ChannelID = "444444444"
	_, e := a.Execute(context.Background(), store.Operation{Kind: "overview", Payload: p, DiscordID: "333333333", BeforeHash: domain.Hash([]byte("original"))})
	if e == nil || patches != 0 {
		t.Fatal("conflicting overview edited")
	}
}
func TestScanLimitDoesNotPermitResend(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		messages := make([]*discordgo.Message, 100)
		for i := range messages {
			messages[i] = &discordgo.Message{ID: "333333333", Content: "different", Author: &discordgo.User{ID: "123456789"}}
		}
		_ = json.NewEncoder(w).Encode(messages)
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	a.BotID = "123456789"
	p := render.NewPayload("desired")
	p.ChannelID = "444444444"
	_, e := a.Reconcile(context.Background(), "555555555", store.Operation{Kind: "message", Payload: p})
	if e == nil || requests != 20 {
		t.Fatalf("scan not bounded or absent considered safe: %v %d", e, requests)
	}
}
func TestForumReconcileIgnoresMemberThread(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/threads/active"):
			_ = json.NewEncoder(w).Encode(map[string]any{"threads": []*discordgo.Channel{{ID: "333333333", ParentID: "444444444", OwnerID: "999999999", Name: "每日題目｜2026-10-09"}}})
		case strings.Contains(r.URL.Path, "/archived/public"):
			_ = json.NewEncoder(w).Encode(map[string]any{"threads": []any{}, "has_more": false})
		default:
			t.Fatal("member-owned thread probed or adopted")
		}
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	a.BotID = "123456789"
	p := render.NewPayload("frozen overview")
	p.ChannelID = "444444444"
	p.Title = "每日題目｜2026-10-09"
	if _, e := a.Reconcile(context.Background(), "555555555", store.Operation{Kind: "forum", Payload: p}); e == nil {
		t.Fatal("member thread adopted")
	}
}

func TestForumReconciliationRequiresCreationWindow(t *testing.T) {
	started := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		created          time.Time
		withEvidence, ok bool
	}{
		{"current", started.Add(time.Second), true, true},
		{"old identical thread", started.Add(-time.Hour), true, false},
		{"later identical thread", started.Add(time.Hour), true, false},
		{"missing send evidence", started, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := strconv.FormatInt((tc.created.UnixMilli()-1420070400000)<<22, 10)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/threads/active"):
					_ = json.NewEncoder(w).Encode(map[string]any{"threads": []*discordgo.Channel{{ID: id, ParentID: "444444444", OwnerID: "123456789", Name: "daily", GuildID: "555555555"}}})
				case strings.Contains(r.URL.Path, "/archived/public"):
					_ = json.NewEncoder(w).Encode(map[string]any{"threads": []any{}, "has_more": false})
				case strings.Contains(r.URL.Path, "/messages/"):
					_ = json.NewEncoder(w).Encode(discordgo.Message{ID: id, Content: "frozen", Author: &discordgo.User{ID: "123456789"}})
				default:
					_ = json.NewEncoder(w).Encode(discordgo.Channel{ID: id, ParentID: "444444444", OwnerID: "123456789", Name: "daily", GuildID: "555555555"})
				}
			}))
			defer server.Close()
			a, _ := New("secret", nil)
			a.BaseURL = server.URL
			a.BotID = "123456789"
			p := render.NewPayload("frozen")
			p.ChannelID = "444444444"
			p.Title = "daily"
			o := store.Operation{Kind: "forum", Payload: p}
			if tc.withEvidence {
				o.SendStartedAt = &started
			}
			if _, e := a.Reconcile(context.Background(), "555555555", o); (e == nil) != tc.ok {
				t.Fatalf("automatic adoption: %v", e)
			}
			if _, e := a.VerifyAdoption(context.Background(), "555555555", o, id); (e == nil) != tc.ok {
				t.Fatalf("manual adoption: %v", e)
			}
		})
	}
}
