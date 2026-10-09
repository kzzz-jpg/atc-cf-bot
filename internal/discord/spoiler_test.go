package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/render"
)

func TestSpoilerReplyProtectsEveryFollowup(t *testing.T) {
	var received []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Content  string                 `json:"content"`
			Flags    discordgo.MessageFlags `json:"flags"`
			Mentions render.Mentions        `json:"allowed_mentions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p.Flags != discordgo.MessageFlagsEphemeral|discordgo.MessageFlagsSuppressEmbeds || len(p.Mentions.Parse) != 0 {
			t.Errorf("unsafe reply flags=%d mentions=%v", p.Flags, p.Mentions)
		}
		if !strings.HasPrefix(p.Content, "||") || !strings.HasSuffix(p.Content, "||") || render.Units(p.Content) > 1800 {
			t.Errorf("unprotected or oversized reply %q", p.Content)
		}
		if len(received) == 0 {
			if r.Method != http.MethodPatch || !strings.HasSuffix(r.URL.Path, "/messages/@original") {
				t.Error("first reply did not edit deferred message")
			}
		} else if r.Method != http.MethodPost {
			t.Error("continuation did not use followup")
		}
		received = append(received, p.Content[2:len(p.Content)-2])
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	text := strings.Repeat("題解😀\n", 1200)
	if err := a.ReplySpoiler(context.Background(), &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}, text); err != nil {
		t.Fatal(err)
	}
	if len(received) < 2 || strings.Join(received, "") != text {
		t.Fatal("missing continuation or answer text")
	}
}

func TestLongSpoilerReplyUsesSpoilerAttachment(t *testing.T) {
	text := strings.Repeat("題解", 20000)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		var p struct {
			Content     string                 `json:"content"`
			Flags       discordgo.MessageFlags `json:"flags"`
			Attachments []struct {
				Filename string `json:"filename"`
			} `json:"attachments"`
		}
		if err := json.Unmarshal([]byte(r.FormValue("payload_json")), &p); err != nil {
			t.Error(err)
		}
		if p.Content != "||完整私人回覆請見附件（AI 產生）。||" || p.Flags != discordgo.MessageFlagsEphemeral|discordgo.MessageFlagsSuppressEmbeds || len(p.Attachments) != 1 || p.Attachments[0].Filename != "SPOILER_answer.txt" {
			t.Errorf("unprotected attachment metadata %+v", p)
		}
		file, header, err := r.FormFile("files[0]")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		body, err := io.ReadAll(file)
		if err != nil || header.Filename != "SPOILER_answer.txt" || string(body) != text {
			t.Error("attachment lost text or spoiler filename")
		}
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	if err := a.ReplySpoiler(context.Background(), &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}, text); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("sent %d requests instead of a single attachment", calls)
	}
}

func TestNormalReplyKeepsCommandFormatting(t *testing.T) {
	text := "設定\n```json\n{}\n```"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Content string                 `json:"content"`
			Flags   discordgo.MessageFlags `json:"flags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p.Content != text || p.Flags != discordgo.MessageFlagsEphemeral {
			t.Errorf("command reply changed %+v", p)
		}
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	if err := a.Reply(context.Background(), &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}, text); err != nil {
		t.Fatal(err)
	}
}
