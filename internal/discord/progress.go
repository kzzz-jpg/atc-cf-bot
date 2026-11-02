package discord

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

const (
	progressColor       = 0x57F287
	backlogPageSize     = 20
	backlogQueryLimit   = 100
	backlogDefaultDays  = 30
	heatmapDefaultWeeks = 8
)

// solveToggle handles the 回報解題 button: pressing it records the solve,
// pressing again removes it.
func (r *Router) solveToggle(ctx context.Context, i *discordgo.Interaction, cfg store.GuildConfig, pub string) (string, bool, error) {
	item, e := r.Store.ButtonSolve(ctx, i.GuildID, i.ChannelID, i.Message.ID, pub)
	if e != nil {
		return "", false, e
	}
	now, e := r.Store.Now(ctx)
	if e != nil {
		return "", false, e
	}
	date := domain.CycleAt(now, cfg.Settings).Date
	added, e := r.Store.ToggleSolve(ctx, i.GuildID, i.Member.User.ID, pub, date, "button")
	if e != nil {
		return "", false, e
	}
	if added {
		return fmt.Sprintf("✅ 已記錄解題：%s %s（%s 起算）。再按一次可取消記錄。", item.Platform, item.ExternalID, date), false, nil
	}
	return fmt.Sprintf("已取消記錄：%s %s。", item.Platform, item.ExternalID), false, nil
}

func intOption(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string, fallback, min, max int) (int, error) {
	v := options[name]
	if v == nil {
		return fallback, nil
	}
	n := int(v.IntValue())
	if n < min || n > max {
		return 0, domain.Fail(domain.Invalid)
	}
	return n, nil
}
func shiftDate(day string, delta int) (string, error) {
	t, e := time.Parse("2006-01-02", day)
	if e != nil {
		return "", domain.Fail(domain.Invalid)
	}
	return t.AddDate(0, 0, delta).Format("2006-01-02"), nil
}

// progressCommand serves /progress me|user|backlog|leaderboard. Rich replies are
// sent here and reported as handled; plain text falls back to the shared reply.
func (r *Router) progressCommand(ctx context.Context, i *discordgo.Interaction, cfg store.GuildConfig, sub string, options map[string]*discordgo.ApplicationCommandInteractionDataOption) (string, bool, error) {
	now, e := r.Store.Now(ctx)
	if e != nil {
		return "", false, e
	}
	today := domain.CycleAt(now, cfg.Settings).Date
	switch sub {
	case "me", "user":
		user, public, heading := i.Member.User.ID, false, "你的解題統計"
		if sub == "user" {
			v := options["user"]
			if v == nil || !domain.Snowflake(v.StringValue()) {
				return "", false, domain.Fail(domain.Invalid)
			}
			user = v.StringValue()
			public = true
			heading = "<@" + user + "> 的解題統計"
		}
		weeks, e := intOption(options, "weeks", heatmapDefaultWeeks, 1, 26)
		if e != nil {
			return "", false, e
		}
		stats, e := r.Store.SolveStats(ctx, i.GuildID, user)
		if e != nil {
			return "", false, e
		}
		description, e := render.ProgressDescription(stats.Days, stats.Total, stats.AtCoder, stats.Codeforces, today, weeks)
		if e != nil {
			return "", false, e
		}
		if e = r.Adapter.ReplyRich(ctx, i, RichReply{Content: heading, Embeds: []render.Embed{{Title: "解題統計", Description: description, Color: progressColor}}, Public: public}); e != nil {
			return "", false, e
		}
		return "", true, nil
	case "backlog":
		days, e := intOption(options, "days", backlogDefaultDays, 1, 366)
		if e != nil {
			return "", false, e
		}
		return r.renderBacklog(ctx, i, cfg, days, 1, "")
	case "leaderboard":
		return r.leaderboard(ctx, i, today, options)
	}
	return "", false, domain.Fail(domain.Invalid)
}

func (r *Router) leaderboard(ctx context.Context, i *discordgo.Interaction, today string, options map[string]*discordgo.ApplicationCommandInteractionDataOption) (string, bool, error) {
	period := "week"
	if v := options["period"]; v != nil {
		period = v.StringValue()
	}
	since, label, e := leaderboardWindow(today, period)
	if e != nil {
		return "", false, e
	}
	entries, e := r.Store.Leaderboard(ctx, i.GuildID, since)
	if e != nil {
		return "", false, e
	}
	var lines []string
	if len(entries) == 0 {
		lines = []string{"這個期間還沒有人回報解題。用題目訊息的「回報解題 ✅」或 /progress backlog 來記錄。"}
	} else {
		for rank, entry := range entries {
			lines = append(lines, fmt.Sprintf("%s <@%s> — %d 題", rankLabel(rank), entry.UserID, entry.Count))
		}
	}
	embed := render.Embed{Title: "解題排行榜（" + label + "）", Description: strings.Join(lines, "\n"), Color: progressColor}
	if e = r.Adapter.ReplyRich(ctx, i, RichReply{Embeds: []render.Embed{embed}, Public: true}); e != nil {
		return "", false, e
	}
	return "", true, nil
}

func rankLabel(rank int) string {
	switch rank {
	case 0:
		return "🥇"
	case 1:
		return "🥈"
	case 2:
		return "🥉"
	}
	return strconv.Itoa(rank+1) + "."
}
func leaderboardWindow(today, period string) (since, label string, e error) {
	t, e := time.Parse("2006-01-02", today)
	if e != nil {
		return "", "", domain.Fail(domain.Invalid)
	}
	switch period {
	case "week":
		offset := (int(t.Weekday()) + 6) % 7
		return t.AddDate(0, 0, -offset).Format("2006-01-02"), "本週", nil
	case "month":
		return today[:8] + "01", "本月", nil
	case "all":
		return "2000-01-01", "全部", nil
	}
	return "", "", domain.Fail(domain.Invalid)
}

// parseBacklogID decodes v1:backlog:pick-<days> and v1:backlog:pg-<page>-<days>.
func parseBacklogID(id string) (kind string, page, days int, e error) {
	p := strings.Split(strings.TrimPrefix(id, "v1:backlog:"), "-")
	switch {
	case len(p) == 2 && p[0] == "pick":
		days, e = strconv.Atoi(p[1])
		kind = "pick"
	case len(p) == 3 && p[0] == "pg":
		page, e = strconv.Atoi(p[1])
		if e == nil {
			days, e = strconv.Atoi(p[2])
		}
		kind = "pg"
	default:
		return "", 0, 0, domain.Fail(domain.Invalid)
	}
	if e != nil || days < 1 || days > 366 || (kind == "pg" && (page < 1 || page > 100)) {
		return "", 0, 0, domain.Fail(domain.Invalid)
	}
	return kind, page, days, nil
}

// backlogComponent serves the select menu and pagination buttons attached to a
// backlog reply.
func (r *Router) backlogComponent(ctx context.Context, i *discordgo.Interaction, cfg store.GuildConfig) (string, bool, error) {
	if i.Message == nil || i.Message.Author == nil || i.Message.Author.ID != r.Adapter.BotID {
		return "", false, domain.Fail(domain.Unauthorized)
	}
	kind, page, days, e := parseBacklogID(i.MessageComponentData().CustomID)
	if e != nil {
		return "", false, e
	}
	if kind == "pg" {
		return r.renderBacklog(ctx, i, cfg, days, page, "")
	}
	values := i.MessageComponentData().Values
	if len(values) == 0 || len(values) > 25 {
		return "", false, domain.Fail(domain.Invalid)
	}
	for _, v := range values {
		if !uuidPattern.MatchString(v) {
			return "", false, domain.Fail(domain.Invalid)
		}
	}
	now, e := r.Store.Now(ctx)
	if e != nil {
		return "", false, e
	}
	today := domain.CycleAt(now, cfg.Settings).Date
	added, e := r.Store.MarkSolves(ctx, i.GuildID, i.Member.User.ID, values, today, "backlog")
	if e != nil {
		return "", false, e
	}
	note := fmt.Sprintf("✅ 已記錄 %d 題解題。", added)
	if added < len(values) {
		note += fmt.Sprintf("（%d 題先前已記錄或已無效，略過。）", len(values)-added)
	}
	return r.renderBacklog(ctx, i, cfg, days, 1, note)
}

func backlogLabel(item store.BacklogItem) string {
	label := strings.Replace(item.Date[5:], "-", "/", 1) + " " + string(item.Platform) + " " + item.ExternalID
	if item.Difficulty != nil {
		label += "・" + strconv.Itoa(*item.Difficulty)
	}
	return label
}

// renderBacklog replies with the user's unsolved problems for the chosen page,
// a multi-select menu for reporting solves and pagination buttons. content is a
// leading note, used by the select handler to confirm what was recorded.
func (r *Router) renderBacklog(ctx context.Context, i *discordgo.Interaction, cfg store.GuildConfig, days, page int, content string) (string, bool, error) {
	now, e := r.Store.Now(ctx)
	if e != nil {
		return "", false, e
	}
	today := domain.CycleAt(now, cfg.Settings).Date
	since, e := shiftDate(today, -(days - 1))
	if e != nil {
		return "", false, e
	}
	items, e := r.Store.Backlog(ctx, i.GuildID, i.Member.User.ID, since, backlogQueryLimit)
	if e != nil {
		return "", false, e
	}
	if len(items) == 0 {
		if content != "" {
			content += "\n"
		}
		return content + "最近 " + strconv.Itoa(days) + " 天內沒有尚未回報的題目。", false, nil
	}
	pages := (len(items) + backlogPageSize - 1) / backlogPageSize
	if page > pages {
		page = pages
	}
	end := page * backlogPageSize
	if end > len(items) {
		end = len(items)
	}
	slice := items[(page-1)*backlogPageSize : end]
	var lines []string
	options := make([]render.SelectOption, 0, len(slice))
	for _, item := range slice {
		line := backlogLabel(item)
		if item.ThreadID != "" && item.MessageID != "" {
			line += fmt.Sprintf("｜[題目訊息](https://discord.com/channels/%s/%s/%s)", i.GuildID, item.ThreadID, item.MessageID)
		}
		lines = append(lines, line)
		description := "回報已寫完"
		if item.Difficulty != nil {
			description = "難度 " + strconv.Itoa(*item.Difficulty)
		}
		options = append(options, render.SelectOption{Label: backlogLabel(item), Value: item.PublicationID, Description: description})
	}
	more := ""
	if len(items) == backlogQueryLimit {
		more = "\n（結果達上限，可能有更早的題目未列出。）"
	}
	embed := render.Embed{Title: fmt.Sprintf("未回報題目（共 %d 題・第 %d/%d 頁）", len(items), page, pages), Description: strings.Join(lines, "\n") + more, Color: progressColor}
	components := []render.Row{
		{Type: 1, Components: []render.Button{{Type: 3, CustomID: "v1:backlog:pick-" + strconv.Itoa(days), Placeholder: "選取已寫完的題目（可多選）", Options: options}}},
		{Type: 1, Components: []render.Button{
			{Type: 2, Style: 2, Label: "◀ 上一頁", CustomID: fmt.Sprintf("v1:backlog:pg-%d-%d", page-1, days), Disabled: page <= 1},
			{Type: 2, Style: 2, Label: "下一頁 ▶", CustomID: fmt.Sprintf("v1:backlog:pg-%d-%d", page+1, days), Disabled: page >= pages},
		}},
	}
	if e = r.Adapter.ReplyRich(ctx, i, RichReply{Content: content, Embeds: []render.Embed{embed}, Components: components}); e != nil {
		return "", false, e
	}
	return "", true, nil
}
