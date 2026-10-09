package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/generation"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

type Router struct {
	Root                                context.Context
	Adapter                             *Adapter
	Store                               *store.Store
	Generation                          *generation.Service
	RecoveryMode                        bool
	Ready                               func() bool
	ManualGuildLimit, ManualGlobalLimit int
	mu                                  sync.Mutex
	cooldown                            map[string]time.Time
	receipts                            map[string]time.Time
	pendingCooldown                     map[string]bool
	stopping                            bool
	gate                                chan struct{}
	wg                                  sync.WaitGroup
}

func (r *Router) Init() {
	r.cooldown = map[string]time.Time{}
	r.receipts = map[string]time.Time{}
	r.pendingCooldown = map[string]bool{}
	r.gate = make(chan struct{}, 200)
	r.Adapter.Session.AddHandler(func(_ *discordgo.Session, event *discordgo.InteractionCreate) { r.Handle(event.Interaction) })
}
func (r *Router) Close() { r.mu.Lock(); r.stopping = true; r.mu.Unlock(); r.wg.Wait() }
func (r *Router) Handle(i *discordgo.Interaction) {
	if r.Root.Err() != nil {
		return
	}
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return
	}
	r.wg.Add(1)
	defer r.wg.Done()
	now := time.Now()
	if _, ok := r.receipts[i.ID]; ok {
		r.mu.Unlock()
		return
	}
	for id, t := range r.receipts {
		if now.After(t) {
			delete(r.receipts, id)
		}
	}
	if len(r.receipts) >= 5000 {
		r.mu.Unlock()
		return
	}
	r.receipts[i.ID] = now.Add(15 * time.Minute)
	r.mu.Unlock()
	ack, c := context.WithTimeout(r.Root, 2500*time.Millisecond)
	e := r.Adapter.Defer(ack, i)
	c()
	if e != nil {
		return
	}
	select {
	case r.gate <- struct{}{}:
	case <-r.Root.Done():
		return
	default:
		ctx, c := context.WithTimeout(r.Root, 5*time.Second)
		_ = r.Adapter.Reply(ctx, i, "目前請求過多，請稍後再試。")
		c()
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() { <-r.gate }()
		ctx, c := context.WithTimeout(r.Root, 180*time.Second)
		defer c()
		text, e := r.dispatch(ctx, i)
		if e != nil {
			text = safeError(e)
		}
		if ctx.Err() != nil {
			clean, cc := context.WithTimeout(r.Root, 5*time.Second)
			_ = r.Adapter.Reply(clean, i, "處理期限已到，請稍後重試或用 /daily show 查詢。")
			cc()
			return
		}
		if e == nil && i.Type == discordgo.InteractionMessageComponent {
			_ = r.Adapter.ReplySpoiler(ctx, i, text)
		} else {
			_ = r.Adapter.Reply(ctx, i, text)
		}
	}()
}
func safeError(e error) string {
	var de *domain.Error
	if errors.As(e, &de) {
		switch de.Code {
		case domain.Unauthorized, domain.ForbiddenActor:
			return "權限不足或此按鈕不屬於可存取的題目。"
		case domain.Invalid:
			return "設定或參數不合法，原設定未變更。"
		case domain.Configuration:
			return "尚未完成設定，請由管理員或部署維護者檢查。"
		case domain.RateLimited:
			return "請求過於頻繁或佇列已滿，請稍後再試。"
		case domain.NoCandidate:
			return "目前沒有符合難度與去重條件的題目。"
		case domain.Conflict:
			return "資料或貼文狀態衝突，請重新查詢；可能需要管理員處理。"
		case domain.Ambiguous:
			return "發布結果需要核對，管理員處理前暫停新公開工作。"
		}
	}
	return "服務暫時無法完成，請稍後重試。"
}
func (r *Router) dispatch(ctx context.Context, i *discordgo.Interaction) (string, error) {
	if i.GuildID == "" || i.Member == nil || i.Member.User == nil {
		return "", domain.Fail(domain.Unauthorized)
	}
	user := i.Member.User.ID
	if e := r.Store.EnsureGuild(ctx, i.GuildID); e != nil {
		return "", e
	}
	cfg, e := r.Store.Config(ctx, i.GuildID)
	if e != nil {
		return "", e
	}
	if e = r.Adapter.Authorize(ctx, i.GuildID, user, i.ChannelID, false, nil); e != nil {
		return "", e
	}
	if i.Type == discordgo.InteractionMessageComponent {
		return r.button(ctx, i, cfg)
	}
	if i.Type != discordgo.InteractionApplicationCommand {
		return "", domain.Fail(domain.Invalid)
	}
	data := i.ApplicationCommandData()
	if data.Name == "help" {
		return "/problem random：在今日討論串加題\n/daily show：查看今日狀態\n/daily publish：管理員補齊每日題目\n/config show、set：管理員查看／修改設定\n題目結尾按鈕：原站、私人提示、私人解答。AI 內容請與原站核對。", nil
	}
	if len(data.Options) != 1 {
		return "", domain.Fail(domain.Invalid)
	}
	sub := data.Options[0]
	admin := data.Name == "config" || (data.Name == "daily" && sub.Name == "publish")
	if admin {
		if e = r.Adapter.Authorize(ctx, i.GuildID, user, i.ChannelID, true, nil); e != nil {
			return "", e
		}
	}
	options := map[string]*discordgo.ApplicationCommandInteractionDataOption{}
	for _, o := range sub.Options {
		options[o.Name] = o
	}
	switch data.Name + " " + sub.Name {
	case "config show":
		raw, _ := json.MarshalIndent(cfg.Settings, "", "  ")
		return fmt.Sprintf("設定版本 %d\n```json\n%s\n```\nDiscord Token：已設定；LLM API key：已設定。", cfg.Version, raw), nil
	case "config set":
		key, value := options["key"], options["value"]
		if key == nil || value == nil {
			return "", domain.Fail(domain.Invalid)
		}
		next, e := cfg.Settings.Set(key.StringValue(), value.StringValue())
		if e != nil {
			return "", e
		}
		if e = r.validateSettings(ctx, i.GuildID, next); e != nil {
			return "", e
		}
		pending, e := r.Store.UpdateConfig(ctx, i.GuildID, cfg.Version, next)
		if e != nil {
			return "", e
		}
		timing := "立即生效；已有每日紀錄的公開設定於下一週期生效"
		if pending {
			timing = "時區於舊時區當日結束後生效"
		}
		raw, _ := json.Marshal(next)
		return fmt.Sprintf("設定已保存（版本 %d）。%s。\n```json\n%s\n```", cfg.Version+1, timing, raw), nil
	case "daily show":
		d, e := r.Store.Today(ctx, i.GuildID)
		if e == pgx.ErrNoRows {
			return "今日尚未建立貼文。", nil
		}
		if e != nil {
			return "", e
		}
		return dailyText(d), nil
	case "daily publish", "problem random":
		if r.Ready != nil && !r.Ready() {
			return "服務正在初始化，請稍後重試。", nil
		}
		if r.RecoveryMode {
			return "備份恢復模式中，暫停接受新公開工作。", nil
		}
		manual := data.Name == "problem"
		if e = r.Adapter.CheckForum(ctx, i.GuildID, cfg.Settings); e != nil {
			return "", e
		}
		if e = r.Adapter.Authorize(ctx, i.GuildID, user, cfg.Settings.ForumChannelID, !manual, cfg.Settings.ManualRoleIDs); e != nil {
			return "", e
		}
		req := store.AcceptRequest{GuildID: i.GuildID, UserID: user, InteractionID: i.ID, Manual: manual, AllowResume: !manual, GuildLimit: r.ManualGuildLimit, GlobalLimit: r.ManualGlobalLimit}
		if src := options["source"]; src != nil {
			req.Source = domain.Platform(src.StringValue())
		}
		if v := options["min_difficulty"]; v != nil {
			n := int(v.IntValue())
			req.Min = &n
		}
		if v := options["max_difficulty"]; v != nil {
			n := int(v.IntValue())
			req.Max = &n
		}
		var finish func(bool)
		if manual {
			var ok bool
			finish, ok = r.beginCooldown(i.GuildID, user, "manual", cfg.Settings.ManualCooldown)
			if !ok {
				return "", domain.Fail(domain.RateLimited)
			}
		}
		job, e := r.Store.Accept(ctx, req)
		if finish != nil {
			finish(e == nil)
		}
		if e != nil {
			return "", e
		}

		return "任務已接受（" + job.Status + "）。請用 /daily show 查看完成狀態。", nil
	}
	return "", domain.Fail(domain.Invalid)
}
func dailyText(d store.Daily) string {
	link := "尚未建立討論串"
	if d.ThreadID != "" {
		link = "https://discord.com/channels/" + d.GuildID + "/" + d.ThreadID
	}
	status := d.Status
	if d.BlockReason != "" {
		status += "；公開工作受阻：" + d.BlockReason
	}
	return fmt.Sprintf("%s 每日題目：%s\n%s", d.Date, status, link)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ParseButton(id string) (string, string, error) {
	p := strings.Split(id, ":")
	if len(p) != 3 || p[0] != "v1" || (p[1] != "hint" && p[1] != "solution") || !uuidPattern.MatchString(p[2]) {
		return "", "", domain.Fail(domain.Invalid)
	}
	return p[1], p[2], nil
}
func (r *Router) button(ctx context.Context, i *discordgo.Interaction, cfg store.GuildConfig) (string, error) {
	kind, pub, e := ParseButton(i.MessageComponentData().CustomID)
	if e != nil {
		return "", e
	}
	if i.Message == nil || i.Message.Author == nil || i.Message.Author.ID != r.Adapter.BotID {
		return "", domain.Fail(domain.Unauthorized)
	}
	sid, e := r.Store.ButtonStatement(ctx, i.GuildID, i.ChannelID, i.Message.ID, pub)
	if e != nil {
		return "", e
	}
	statement, e := r.Store.Statement(ctx, sid)
	if e != nil {
		return "", e
	}
	finish, ok := r.beginCooldown(i.GuildID, i.Member.User.ID, "generation", cfg.Settings.GenerationCooldown)
	if !ok {
		return "", domain.Fail(domain.RateLimited)
	}
	raw, _, e := r.Generation.Generate(ctx, i.GuildID, sid, kind, cfg.Settings.SolutionLanguage, statement)
	finish(e == nil || !isRateLimited(e))
	if e != nil {
		return "", e
	}
	var result llm.Result
	if json.Unmarshal(raw, &result) != nil {
		return "", domain.Fail(domain.Unsupported)
	}
	return result.Text, nil
}
func isRateLimited(e error) bool {
	var de *domain.Error
	return errors.As(e, &de) && de.Code == domain.RateLimited
}
func (r *Router) beginCooldown(guild, user, group string, seconds int) (func(bool), bool) {
	key := guild + ":" + user + ":" + group
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for k, t := range r.cooldown {
		if !now.Before(t) {
			delete(r.cooldown, k)
		}
	}
	if r.pendingCooldown[key] || now.Before(r.cooldown[key]) || len(r.cooldown) >= 10000 {
		return nil, false
	}
	r.pendingCooldown[key] = true
	return func(accepted bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.pendingCooldown, key)
		if accepted {
			r.cooldown[key] = time.Now().Add(time.Duration(seconds) * time.Second)
		}
	}, true
}
func (r *Router) validateSettings(ctx context.Context, guild string, s domain.Settings) error {
	if s.ForumChannelID != "" {
		if e := r.Adapter.CheckForum(ctx, guild, s); e != nil {
			return e
		}
	}
	if s.AdminLogChannelID != "" {
		ch, e := r.Adapter.Channel(ctx, s.AdminLogChannelID)
		if e != nil || ch.GuildID != guild {
			return domain.Fail(domain.Invalid)
		}
		p, _, e := r.Adapter.Permissions(ctx, guild, r.Adapter.BotID, ch.ID)
		if e != nil || p&discordgo.PermissionSendMessages == 0 {
			return domain.Fail(domain.Unauthorized)
		}
	}
	if len(s.ManualRoleIDs) > 0 {
		var roles []*discordgo.Role
		if e := r.Adapter.JSON(ctx, "GET", "/guilds/"+guild+"/roles", "guild:"+guild, nil, &roles, false); e != nil {
			return e
		}
		known := map[string]bool{}
		for _, role := range roles {
			known[role.ID] = true
		}
		for _, id := range s.ManualRoleIDs {
			if !known[id] {
				return domain.Fail(domain.Invalid)
			}
		}
	}
	return nil
}
