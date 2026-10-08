package discord

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/source"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

func (a *Adapter) Match(ctx context.Context, m *discordgo.Message, o store.Operation) bool {
	if m == nil || m.Author == nil || m.Author.ID != a.BotID || m.Content != o.Payload.Content || len(m.Attachments) != len(o.Payload.Attachments) || len(m.Components) != len(o.Payload.Components) || len(m.Embeds) != len(o.Payload.Embeds) {
		return false
	}
	for i, row := range o.Payload.Components {
		actual, ok := m.Components[i].(*discordgo.ActionsRow)
		if !ok {
			value, yes := m.Components[i].(discordgo.ActionsRow)
			if !yes {
				return false
			}
			actual = &value
		}
		if len(actual.Components) != len(row.Components) {
			return false
		}
		for j, button := range row.Components {
			var b discordgo.Button
			switch v := actual.Components[j].(type) {
			case discordgo.Button:
				b = v
			case *discordgo.Button:
				b = *v
			default:
				return false
			}
			if b.Label != button.Label || int(b.Style) != button.Style || b.CustomID != button.CustomID || b.URL != button.URL {
				return false
			}
		}
	}
	for i, expected := range o.Payload.Embeds {
		actual := m.Embeds[i]
		if expected.Image != nil {
			if actual == nil || actual.Image == nil {
				return false
			}
			filename := strings.TrimPrefix(expected.Image.URL, "attachment://")
			matched := false
			for _, attachment := range m.Attachments {
				if attachment.Filename == filename && actual.Image.URL == attachment.URL {
					matched = true
				}
			}
			if !matched {
				return false
			}
		}
	}
	for i, at := range o.Payload.Attachments {
		got := m.Attachments[i]
		if got.Filename != at.Filename {
			return false
		}
		expected, _, e := a.Assets.Asset(ctx, at.AssetID)
		if e != nil || got.Size != len(expected) {
			return false
		}
		f := source.NewFetcher()
		f.Hosts = map[string]bool{"cdn.discordapp.com": true, "media.discordapp.net": true}
		body, _, e := f.Get(ctx, got.URL, int64(len(expected)))
		f.Client.CloseIdleConnections()
		if e != nil || domain.Hash(body) != domain.Hash(expected) {
			return false
		}
	}
	return true
}
func (a *Adapter) Reconcile(ctx context.Context, guild string, o store.Operation) (store.DiscordResult, error) {
	if o.Kind == "overview" {
		m, e := a.Message(ctx, o.Payload.ChannelID, o.DiscordID)
		if e != nil {
			return store.DiscordResult{}, e
		}
		if a.Match(ctx, m, o) {
			return store.DiscordResult{ID: m.ID}, nil
		}
		return store.DiscordResult{}, domain.Fail(domain.Conflict)
	}
	if o.Kind == "forum" {
		return a.reconcileForum(ctx, guild, o)
	}
	before := ""
	var matches []*discordgo.Message
	for page := 0; page < 20; page++ {
		var messages []*discordgo.Message
		path := "/channels/" + o.Payload.ChannelID + "/messages?limit=100"
		if before != "" {
			path += "&before=" + before
		}
		if e := a.JSON(ctx, "GET", path, "channels:"+o.Payload.ChannelID, nil, &messages, false); e != nil {
			return store.DiscordResult{}, e
		}
		for _, m := range messages {
			if a.Match(ctx, m, o) {
				matches = append(matches, m)
			}
		}
		if len(messages) < 100 {
			if len(matches) != 1 {
				return store.DiscordResult{}, domain.Fail(domain.Ambiguous)
			}
			return store.DiscordResult{ID: matches[0].ID}, nil
		}
		before = messages[len(messages)-1].ID
	}
	return store.DiscordResult{}, domain.Fail(domain.Ambiguous)
}
func (a *Adapter) reconcileForum(ctx context.Context, guild string, o store.Operation) (store.DiscordResult, error) {
	var active struct {
		Threads []*discordgo.Channel `json:"threads"`
	}
	if e := a.JSON(ctx, "GET", "/guilds/"+guild+"/threads/active", "guild:"+guild, nil, &active, false); e != nil {
		return store.DiscordResult{}, e
	}
	candidates := map[string]*discordgo.Channel{}
	for _, t := range active.Threads {
		if t.ParentID == o.Payload.ChannelID {
			candidates[t.ID] = t
		}
	}
	before := ""
	complete := false
	for page := 0; page < 20; page++ {
		var archived struct {
			Threads []*discordgo.Channel `json:"threads"`
			HasMore bool                 `json:"has_more"`
		}
		path := "/channels/" + o.Payload.ChannelID + "/threads/archived/public?limit=100"
		if before != "" {
			path += "&before=" + url.QueryEscape(before)
		}
		if e := a.JSON(ctx, "GET", path, "channels:"+o.Payload.ChannelID, nil, &archived, false); e != nil {
			return store.DiscordResult{}, e
		}
		for _, t := range archived.Threads {
			candidates[t.ID] = t
		}
		if !archived.HasMore {
			complete = true
			break
		}
		if len(archived.Threads) == 0 || archived.Threads[len(archived.Threads)-1].ThreadMetadata == nil {
			break
		}
		before = archived.Threads[len(archived.Threads)-1].ThreadMetadata.ArchiveTimestamp.Format(time.RFC3339Nano)
	}
	if !complete {
		return store.DiscordResult{}, domain.Fail(domain.Ambiguous)
	}
	var matches []store.DiscordResult
	for _, t := range candidates {
		if t.Name != o.Payload.Title || t.OwnerID != a.BotID || t.ParentID != o.Payload.ChannelID || !withinSendWindow(t.ID, o) {
			continue
		}
		m, e := a.Message(ctx, t.ID, t.ID)
		if e != nil {
			continue
		}
		if a.Match(ctx, m, o) {
			matches = append(matches, store.DiscordResult{ID: t.ID, ThreadID: t.ID, StarterID: m.ID})
		}
	}
	if len(matches) != 1 {
		return store.DiscordResult{}, domain.Fail(domain.Ambiguous)
	}
	return matches[0], nil
}

// Creation time comes from Discord's snowflake, even after the thread is archived.
// Missing send evidence stays uncertain. Allow modest host clock skew and a
// bounded request completion delay; restarting later cannot widen this window.
func withinSendWindow(id string, o store.Operation) bool {
	if o.SendStartedAt == nil {
		return false
	}
	created, e := discordgo.SnowflakeTimestamp(id)
	return e == nil && !created.Before(o.SendStartedAt.Add(-2*time.Minute)) && !created.After(o.SendStartedAt.Add(10*time.Minute))
}

func (a *Adapter) VerifyAdoption(ctx context.Context, guild string, o store.Operation, id string) (store.DiscordResult, error) {
	if !domain.Snowflake(id) {
		return store.DiscordResult{}, domain.Fail(domain.Invalid)
	}
	channel := o.Payload.ChannelID
	if o.Kind == "forum" {
		channel = id
		ch, e := a.Channel(ctx, id)
		if e != nil || ch.GuildID != guild || ch.ParentID != o.Payload.ChannelID || ch.OwnerID != a.BotID || ch.Name != o.Payload.Title || !withinSendWindow(id, o) {
			return store.DiscordResult{}, domain.Fail(domain.Conflict)
		}
	}
	m, e := a.Message(ctx, channel, id)
	if e != nil || !a.Match(ctx, m, o) {
		return store.DiscordResult{}, domain.Fail(domain.Conflict)
	}
	r := store.DiscordResult{ID: id}
	if o.Kind == "forum" {
		r.ThreadID = id
		r.StarterID = m.ID
	}
	return r, nil
}
