package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

func TestOverviewUpdateAcceptsRemovedTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		ok      bool
	}{
		{"unchanged", "previous", true},
		{"edited", "someone changed the overview", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var edits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(discordgo.Message{ID: "123456789", Content: tc.content, Author: &discordgo.User{ID: "987654321"}})
					return
				}
				if r.Method != http.MethodPatch {
					t.Errorf("unexpected method %s", r.Method)
				}
				edits.Add(1)
				_, _ = w.Write([]byte(`{"id":"123456789"}`))
			}))
			defer server.Close()
			a, _ := New("secret", nil)
			a.BaseURL = server.URL
			a.BotID = "987654321"
			payload := render.NewPayload("completed")
			payload.ChannelID = "555555555"
			_, e := a.Execute(context.Background(), store.Operation{Kind: "overview", DiscordID: "123456789", BeforeHash: domain.Hash([]byte("previous\n")), Payload: payload})
			if (e == nil) != tc.ok || (edits.Load() == 1) != tc.ok {
				t.Fatalf("error=%v edits=%d", e, edits.Load())
			}
		})
	}
}

func TestHistoricalOverviewMatchesRemovedTrailingNewline(t *testing.T) {
	a, _ := New("secret", nil)
	a.BotID = "987654321"
	m := &discordgo.Message{ID: "123456789", Content: "completed", Author: &discordgo.User{ID: a.BotID}}
	o := store.Operation{Kind: "overview", Payload: render.NewPayload("completed\n")}
	if !a.Match(context.Background(), m, o) {
		t.Fatal("historical overview did not match normalized content")
	}
	o.Kind = "message"
	if a.Match(context.Background(), m, o) {
		t.Fatal("problem message matching was relaxed")
	}
}

func TestPublicCreateHasNoHiddenRetry(t *testing.T) {
	for _, status := range []int{500, 429, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"retry_after":0.01}`))
			}))
			defer server.Close()
			a, _ := New("secret", nil)
			a.BaseURL = server.URL
			p := render.NewPayload("題目")
			p.ChannelID = "123456789"
			_, e := a.Execute(context.Background(), store.Operation{Kind: "message", Payload: p})
			var de *domain.Error
			if !errors.As(e, &de) || count.Load() != 1 {
				t.Fatalf("err %v count %d", e, count.Load())
			}
			if status == 500 && de.Code != domain.Ambiguous {
				t.Fatal("5xx must be ambiguous")
			}
		})
	}
}
func TestBucketWaitCancellation(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "60")
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	var result map[string]any
	if e := a.JSON(context.Background(), "GET", "/test", "bucket", nil, &result, false); e != nil {
		t.Fatal(e)
	}
	ctx, c := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer c()
	started := time.Now()
	if e := a.JSON(ctx, "GET", "/test", "bucket", nil, &result, false); e == nil || time.Since(started) > time.Second || count.Load() != 1 {
		t.Fatal("rate wait ignored cancellation")
	}
}
func TestLostResponseAndNonceContract(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["nonce"] != "stable" || payload["enforce_nonce"] != true {
			t.Error("nonce contract missing")
		}
		mentions := payload["allowed_mentions"].(map[string]any)
		if len(mentions["parse"].([]any)) != 0 {
			t.Error("mentions enabled")
		}
		h := w.(http.Hijacker)
		conn, _, _ := h.Hijack()
		_ = conn.Close()
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	p := render.NewPayload("@everyone")
	p.Nonce = "stable"
	p.ChannelID = "123456789"
	_, e := a.Execute(context.Background(), store.Operation{Kind: "message", Payload: p})
	var de *domain.Error
	if !errors.As(e, &de) || de.Code != domain.Ambiguous || count.Load() != 1 {
		t.Fatal("lost response retried or misclassified")
	}
}
func TestCustomID(t *testing.T) {
	id := domain.NewID()
	if k, p, e := ParseButton("v1:hint:" + id); e != nil || k != "hint" || p != id {
		t.Fatal("route")
	}
	for _, s := range []string{"v2:hint:" + id, "v1:hint:secret", "v1:pick:" + id, "v1:hint:" + id + ":extra"} {
		if _, _, e := ParseButton(s); e == nil {
			t.Fatal("invalid custom id")
		}
	}
}
func TestCooldownAdmissionIsAtomic(t *testing.T) {
	r := Router{cooldown: map[string]time.Time{}, pendingCooldown: map[string]bool{}}
	finish, ok := r.beginCooldown("g", "u", "manual", 60)
	if !ok {
		t.Fatal("first admission rejected")
	}
	if _, ok = r.beginCooldown("g", "u", "manual", 60); ok {
		t.Fatal("concurrent duplicate accepted")
	}
	finish(false)
	finish, ok = r.beginCooldown("g", "u", "manual", 60)
	if !ok {
		t.Fatal("rejection consumed cooldown")
	}
	finish(true)
	if _, ok = r.beginCooldown("g", "u", "manual", 60); ok {
		t.Fatal("cooldown ignored")
	}
}
func TestSDKBucketWaitRespectsContext(t *testing.T) {
	a, _ := New("secret", nil)
	b := a.Session.Ratelimiter.GetBucket("channels:123456789")
	b.Lock()
	b.Remaining = 0
	_ = b.Release(http.Header{"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset-After": []string{"60"}})
	ctx, c := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer c()
	started := time.Now()
	if e := a.JSON(ctx, "GET", "/unreachable", "channels:123456789", nil, nil, false); e == nil || time.Since(started) > time.Second {
		t.Fatal("SDK bucket wait not cancellable")
	}
}
