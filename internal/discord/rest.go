// Package discord uses discordgo for Gateway events and an explicit REST adapter
// for bounded, context-aware requests with no hidden create retries.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/render"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

type assetStore interface {
	Asset(context.Context, string) ([]byte, string, error)
}
type bucket struct {
	gate     chan struct{}
	until    time.Time
	lastUsed time.Time
}
type Adapter struct {
	Session                              *discordgo.Session
	HTTP                                 *http.Client
	BaseURL, BotID, ApplicationID, Token string
	Assets                               assetStore
	mu                                   sync.Mutex
	buckets                              map[string]*bucket
	globalUntil                          time.Time
}

func New(token string, assets assetStore) (*Adapter, error) {
	s, e := discordgo.New("Bot " + token)
	if e != nil {
		return nil, domain.Fail(domain.Configuration)
	}
	s.Identify.Intents = discordgo.IntentsGuilds
	s.ShouldRetryOnRateLimit = false
	s.MaxRestRetries = 0
	s.LogLevel = -1
	return &Adapter{Session: s, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, BaseURL: "https://discord.com/api/v10", Token: token, Assets: assets, buckets: map[string]*bucket{}}, nil
}
func (a *Adapter) request(ctx context.Context, method, path, route string, body []byte, contentType string, out any, public bool) error {
	a.mu.Lock()
	now := time.Now()
	for key, old := range a.buckets {
		if now.Sub(old.lastUsed) > 10*time.Minute && len(old.gate) == 0 {
			delete(a.buckets, key)
		}
	}
	b := a.buckets[route]
	if b == nil {
		b = &bucket{gate: make(chan struct{}, 1)}
		a.buckets[route] = b
	}
	b.lastUsed = now
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return domain.Fail(domain.Transient)
	case b.gate <- struct{}{}:
	}
	defer func() { <-b.gate }()
	a.mu.Lock()
	until := b.until
	if a.globalUntil.After(until) {
		until = a.globalUntil
	}
	a.mu.Unlock()
	if e := llm.Wait(ctx, time.Until(until)); e != nil {
		return domain.Fail(domain.Transient)
	}
	// Keep discordgo's stable-route bucket accounting, replacing its blocking
	// LockBucketObject/time.Sleep with context-aware acquisition and waiting.
	var sdkBucket *discordgo.Bucket
	var sdkHeaders http.Header
	if !strings.HasPrefix(route, "interaction:") && !strings.HasPrefix(route, "webhook:") {
		sdkBucket = a.Session.Ratelimiter.GetBucket(route)
		for !sdkBucket.TryLock() {
			if e := llm.Wait(ctx, 5*time.Millisecond); e != nil {
				return domain.Fail(domain.Transient)
			}
		}
		defer func() { _ = sdkBucket.Release(sdkHeaders) }()
		if e := llm.Wait(ctx, a.Session.Ratelimiter.GetWaitTime(sdkBucket, 1)); e != nil {
			return domain.Fail(domain.Transient)
		}
		sdkBucket.Remaining--
	}
	req, e := http.NewRequestWithContext(ctx, method, a.BaseURL+path, bytes.NewReader(body))
	if e != nil {
		return domain.Fail(domain.Invalid)
	}
	req.Header.Set("Authorization", "Bot "+a.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, e := a.HTTP.Do(req)
	if e != nil {
		code := domain.Transient
		if public {
			code = domain.Ambiguous
		}
		return domain.Fail(code)
	}
	defer res.Body.Close()
	sdkHeaders = res.Header
	if res.Header.Get("X-RateLimit-Remaining") == "0" {
		delay := llm.RetryAfter(res.Header.Get("X-RateLimit-Reset-After"), time.Now())
		a.mu.Lock()
		b.until = time.Now().Add(delay)
		a.mu.Unlock()
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		code := domain.Transient
		if public {
			code = domain.Ambiguous
		}
		return domain.Fail(code)
	}
	if res.StatusCode == 429 {
		var limit struct {
			RetryAfter float64 `json:"retry_after"`
			Global     bool    `json:"global"`
		}
		_ = json.Unmarshal(raw, &limit)
		delay := time.Duration(limit.RetryAfter * float64(time.Second))
		if delay <= 0 {
			delay = llm.RetryAfter(res.Header.Get("Retry-After"), time.Now())
		}
		if delay <= 0 {
			delay = time.Second
		}
		a.mu.Lock()
		b.until = time.Now().Add(delay)
		if limit.Global {
			a.globalUntil = b.until
		}
		a.mu.Unlock()
		return &domain.Error{Code: domain.RateLimited, Status: 429, RetryAfter: delay}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := domain.Unsupported
		switch {
		case res.StatusCode >= 500:
			code = domain.Transient
			if public {
				code = domain.Ambiguous
			}
		case res.StatusCode == 401 || res.StatusCode == 403:
			code = domain.Unauthorized
		case res.StatusCode == 404:
			code = domain.Conflict
		}
		return &domain.Error{Code: code, Status: res.StatusCode}
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		code := domain.Unsupported
		if public {
			code = domain.Ambiguous
		}
		return domain.Fail(code)
	}
	return nil
}
func (a *Adapter) JSON(ctx context.Context, method, path, route string, payload, out any, public bool) error {
	var b []byte
	var e error
	if payload != nil {
		b, e = json.Marshal(payload)
		if e != nil {
			return domain.Fail(domain.Invalid)
		}
	}
	return a.request(ctx, method, path, route, b, "application/json", out, public)
}
func (a *Adapter) Execute(ctx context.Context, o store.Operation) (store.DiscordResult, error) {
	p := o.Payload
	if o.Kind == "overview" {
		m, e := a.Message(ctx, p.ChannelID, o.DiscordID)
		if e != nil {
			return store.DiscordResult{}, e
		}
		if m.Author == nil || m.Author.ID != a.BotID {
			return store.DiscordResult{}, domain.Fail(domain.Conflict)
		}
		if a.Match(ctx, m, o) {
			return store.DiscordResult{ID: m.ID}, nil
		}
		// Older saved overviews ended with a newline that Discord removes.
		// Verify that exact historical content too before updating it.
		if domain.Hash([]byte(m.Content)) != o.BeforeHash && domain.Hash([]byte(m.Content+"\n")) != o.BeforeHash {
			return store.DiscordResult{}, domain.Fail(domain.Conflict)
		}
	}
	wire := map[string]any{"content": p.Content, "allowed_mentions": p.AllowedMentions, "components": p.Components, "embeds": p.Embeds, "attachments": p.Attachments}
	// AssetID is local metadata, not a Discord field.
	attachments := make([]map[string]any, len(p.Attachments))
	for i, at := range p.Attachments {
		attachments[i] = map[string]any{"id": at.ID, "filename": at.Filename}
	}
	wire["attachments"] = attachments
	if o.Kind == "message" {
		wire["nonce"] = p.Nonce
		wire["enforce_nonce"] = true
	}
	path := "/channels/" + p.ChannelID + "/messages"
	route := "channels:" + p.ChannelID
	method := "POST"
	payload := any(wire)
	if o.Kind == "forum" {
		path = "/channels/" + p.ChannelID + "/threads"
		payload = map[string]any{"name": p.Title, "auto_archive_duration": 1440, "message": wire, "applied_tags": p.Tags}
	}
	if o.Kind == "overview" {
		path += "/" + o.DiscordID
		method = "PATCH"
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return store.DiscordResult{}, e
	}
	ct := "application/json"
	if len(p.Attachments) > 0 && o.Kind != "overview" {
		var data bytes.Buffer
		mw := multipart.NewWriter(&data)
		field, _ := mw.CreateFormField("payload_json")
		_, _ = field.Write(raw)
		for i, at := range p.Attachments {
			body, mime, e := a.Assets.Asset(ctx, at.AssetID)
			if e != nil {
				return store.DiscordResult{}, e
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[%d]"; filename="%s"`, i, at.Filename))
			h.Set("Content-Type", mime)
			part, e := mw.CreatePart(h)
			if e != nil {
				return store.DiscordResult{}, e
			}
			_, _ = part.Write(body)
		}
		_ = mw.Close()
		raw = data.Bytes()
		ct = mw.FormDataContentType()
	}
	var result struct {
		ID          string                         `json:"id"`
		Message     *discordgo.Message             `json:"message"`
		Attachments []*discordgo.MessageAttachment `json:"attachments"`
	}
	e = a.request(ctx, method, path, route, raw, ct, &result, true)
	if e != nil {
		return store.DiscordResult{}, e
	}
	if !domain.Snowflake(result.ID) {
		return store.DiscordResult{}, domain.Fail(domain.Ambiguous)
	}
	r := store.DiscordResult{ID: result.ID}
	if o.Kind == "forum" {
		if result.Message == nil || !domain.Snowflake(result.Message.ID) {
			return r, domain.Fail(domain.Ambiguous)
		}
		r.ThreadID = result.ID
		r.StarterID = result.Message.ID
	}
	for _, at := range result.Attachments {
		r.Attachments = append(r.Attachments, render.Attachment{ID: at.ID, Filename: at.Filename})
	}
	return r, nil
}
func (a *Adapter) Channel(ctx context.Context, id string) (*discordgo.Channel, error) {
	var ch discordgo.Channel
	e := a.JSON(ctx, "GET", "/channels/"+id, "channels:"+id, nil, &ch, false)
	return &ch, e
}
func (a *Adapter) Message(ctx context.Context, channel, id string) (*discordgo.Message, error) {
	var m discordgo.Message
	e := a.JSON(ctx, "GET", "/channels/"+channel+"/messages/"+id, "channels:"+channel, nil, &m, false)
	return &m, e
}
func (a *Adapter) Identity(ctx context.Context) error {
	var user discordgo.User
	if e := a.JSON(ctx, "GET", "/users/@me", "identity", nil, &user, false); e != nil {
		return e
	}
	a.BotID = user.ID
	var app discordgo.Application
	if e := a.JSON(ctx, "GET", "/oauth2/applications/@me", "identity", nil, &app, false); e != nil {
		return e
	}
	a.ApplicationID = app.ID
	return nil
}
func (a *Adapter) Defer(ctx context.Context, i *discordgo.Interaction) error {
	return a.JSON(ctx, "POST", "/interactions/"+i.ID+"/"+i.Token+"/callback", "interaction:"+i.ID, map[string]any{"type": 5, "data": map[string]any{"flags": 64, "allowed_mentions": render.Mentions{Parse: []string{}}}}, nil, false)
}
func (a *Adapter) Reply(ctx context.Context, i *discordgo.Interaction, text string) error {
	return a.reply(ctx, i, text, false)
}
func (a *Adapter) ReplySpoiler(ctx context.Context, i *discordgo.Interaction, text string) error {
	return a.reply(ctx, i, text, true)
}
func (a *Adapter) reply(ctx context.Context, i *discordgo.Interaction, text string, spoiler bool) error {
	var parts []string
	var e error
	flags := discordgo.MessageFlagsEphemeral
	if spoiler {
		parts, e = render.SplitSpoiler(text, 1800)
		flags |= discordgo.MessageFlagsSuppressEmbeds
	} else {
		parts, e = render.SplitMarkdown(text, 1800, nil)
	}
	if e != nil || len(parts) > 20 {
		return a.privateAttachment(ctx, i, text, spoiler)
	}
	for n, p := range parts {
		path := "/webhooks/" + i.AppID + "/" + i.Token
		method := "POST"
		payload := map[string]any{"content": p, "flags": flags, "allowed_mentions": render.Mentions{Parse: []string{}}}
		if n == 0 {
			path += "/messages/@original"
			method = "PATCH"
		}
		if e = a.JSON(ctx, method, path, "webhook:"+i.ID, payload, nil, false); e != nil {
			return e
		}
	}
	return nil
}
func (a *Adapter) PrivateAttachment(ctx context.Context, i *discordgo.Interaction, text string) error {
	return a.privateAttachment(ctx, i, text, false)
}
func (a *Adapter) privateAttachment(ctx context.Context, i *discordgo.Interaction, text string, spoiler bool) error {
	var data bytes.Buffer
	mw := multipart.NewWriter(&data)
	field, _ := mw.CreateFormField("payload_json")
	filename := "answer.txt"
	content := "完整私人回覆請見附件（AI 產生）。"
	flags := discordgo.MessageFlagsEphemeral
	if spoiler {
		filename = "SPOILER_" + filename
		content = "||" + content + "||"
		flags |= discordgo.MessageFlagsSuppressEmbeds
	}
	payload := map[string]any{"content": content, "flags": flags, "allowed_mentions": render.Mentions{Parse: []string{}}, "attachments": []any{map[string]any{"id": "0", "filename": filename}}}
	raw, _ := json.Marshal(payload)
	_, _ = field.Write(raw)
	file, e := mw.CreateFormFile("files[0]", filename)
	if e != nil {
		return e
	}
	_, _ = file.Write([]byte(text))
	_ = mw.Close()
	return a.request(ctx, "PATCH", "/webhooks/"+i.AppID+"/"+i.Token+"/messages/@original", "webhook:"+i.ID, data.Bytes(), mw.FormDataContentType(), nil, false)
}
