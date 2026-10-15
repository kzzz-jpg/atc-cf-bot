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

func TestShortAnswerUsesOneReply(t *testing.T) {
	text := "## 演算法\n\n**觀察**：加總 `a_i`。\n\n```latex\nS = \\sum_i a_i\n```"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var p struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPatch || !strings.HasSuffix(r.URL.Path, "/messages/@original") || p.Content != text {
			t.Errorf("unexpected answer reply: %s %s %q", r.Method, r.URL.Path, p.Content)
		}
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	if err := a.ReplyAnswer(context.Background(), &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}, text); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("single answer produced %d replies", calls)
	}
}

func TestAnswerReplyPreservesMarkdownInOnePrivateMessage(t *testing.T) {
	var received []string
	text := strings.Repeat("## 觀察\n\n**重點**：考慮 `N`。\n\n```latex\n\\sum_{i=1}^{N} a_i\n```\n\n", 80)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var p struct {
			Content  string                 `json:"content"`
			Flags    discordgo.MessageFlags `json:"flags"`
			Mentions render.Mentions        `json:"allowed_mentions"`
			Embeds   []struct {
				Description string `json:"description"`
			} `json:"embeds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p.Flags != discordgo.MessageFlagsEphemeral || len(p.Mentions.Parse) != 0 {
			t.Errorf("unsafe reply flags=%d mentions=%v", p.Flags, p.Mentions)
		}
		if p.Content != "" || len(p.Embeds) == 0 {
			t.Error("long answer missing embeds or duplicated in content")
		}
		if r.Method != http.MethodPatch || !strings.HasSuffix(r.URL.Path, "/messages/@original") {
			t.Error("answer created an extra message")
		}
		units := 0
		for _, embed := range p.Embeds {
			body := embed.Description
			units += render.Units(body)
			if render.Units(body) > 4096 || strings.Count(body, "```")%2 != 0 {
				t.Error("oversized embed or broken LaTeX fence")
			}
			received = append(received, body)
		}
		if units > 6000 {
			t.Error("embed total exceeds Discord limit")
		}
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	if err := a.ReplyAnswer(context.Background(), &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}, text); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(received) < 2 || strings.Join(received, "") != text {
		t.Fatal("duplicated message or lost answer text")
	}
	// A repeated delivery must edit the same message, with no followup POST.
	received = nil
	if err := a.ReplyAnswer(context.Background(), &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}, text); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Join(received, "") != text {
		t.Fatal("repeated delivery changed the answer")
	}
}

func TestLongAnswerReplyUsesPrivateAttachment(t *testing.T) {
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
		if p.Content != "完整私人回覆請見附件（AI 產生）。" || p.Flags != discordgo.MessageFlagsEphemeral|discordgo.MessageFlagsSuppressEmbeds || len(p.Attachments) != 1 || p.Attachments[0].Filename != "answer.txt" {
			t.Errorf("incorrect attachment metadata %+v", p)
		}
		file, header, err := r.FormFile("files[0]")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		body, err := io.ReadAll(file)
		if err != nil || header.Filename != "answer.txt" || string(body) != text {
			t.Error("attachment lost text or filename")
		}
		_, _ = w.Write([]byte(`{"id":"123456789"}`))
	}))
	defer server.Close()
	a, _ := New("secret", nil)
	a.BaseURL = server.URL
	if err := a.ReplyAnswer(context.Background(), &discordgo.Interaction{ID: "123456789", AppID: "987654321", Token: "interaction"}, text); err != nil {
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
