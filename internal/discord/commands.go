package discord

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

func Commands() []*discordgo.ApplicationCommand {
	contexts := []discordgo.InteractionContextType{discordgo.InteractionContextGuild}
	installs := []discordgo.ApplicationIntegrationType{0}
	admin := int64(discordgo.PermissionManageServer)
	sub := func(name, desc string, options ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: name, Description: desc, Options: options}
	}
	cmds := []*discordgo.ApplicationCommand{
		{Name: "problem", Description: "抽取競賽題目", Options: []*discordgo.ApplicationCommandOption{sub("random", "在今日討論串加一題",
			&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "source", Description: "題目來源", Choices: []*discordgo.ApplicationCommandOptionChoice{{Name: "AtCoder", Value: "atcoder"}, {Name: "Codeforces", Value: "codeforces"}}},
			&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "min_difficulty", Description: "最低難度，需指定來源"},
			&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_difficulty", Description: "最高難度，需指定來源"})}},
		{Name: "daily", Description: "每日題目", Options: []*discordgo.ApplicationCommandOption{sub("show", "查看今日狀態"), sub("publish", "執行或恢復今日每日題目（管理員）")}},
		{Name: "config", Description: "管理設定", DefaultMemberPermissions: &admin, Options: []*discordgo.ApplicationCommandOption{sub("show", "查看設定"), sub("set", "更新設定",
			&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "key", Description: "設定欄位", Required: true},
			&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "value", Description: "設定值，清單使用 JSON array", Required: true})}},
		{Name: "help", Description: "指令與按鈕使用說明"},
	}
	for _, cmd := range cmds {
		cmd.Type = discordgo.ChatApplicationCommand
		cmd.Contexts = &contexts
		cmd.IntegrationTypes = &installs
	}
	return cmds
}
func (a *Adapter) Register(ctx context.Context, guild string) error {
	path := "/applications/" + a.ApplicationID
	if guild != "" {
		path += "/guilds/" + guild
	}
	path += "/commands"
	var current []*discordgo.ApplicationCommand
	if e := a.JSON(ctx, "GET", path, "commands", nil, &current, false); e != nil {
		return e
	}
	desired := Commands()
	trim := func(cmds []*discordgo.ApplicationCommand) map[string]string {
		out := map[string]string{}
		for _, c := range cmds {
			c.ID = ""
			c.GuildID = ""
			c.ApplicationID = ""
			c.Version = ""
			c.DefaultPermission = nil
			c.DMPermission = nil
			c.NSFW = nil
			if guild != "" {
				c.Contexts = nil
				c.IntegrationTypes = nil
			}
			out[c.Name] = domain.JSONHash(c)
		}
		return out
	}
	if domain.JSONHash(trim(current)) == domain.JSONHash(trim(desired)) {
		return nil
	}
	return a.JSON(ctx, "PUT", path, "commands", desired, nil, false)
}
