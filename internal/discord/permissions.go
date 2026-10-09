package discord

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

// Owners and administrators have every permission, including newer flags
// omitted from discordgo.PermissionAll, and bypass channel overwrites.
const allPermissions int64 = (1 << 63) - 1

// Permissions always fetches current roles/member/channel rather than trusting
// an interaction's earlier permissions or an incomplete Gateway member cache.
func (a *Adapter) Permissions(ctx context.Context, guild, user, channel string) (int64, []string, error) {
	ch, e := a.Channel(ctx, channel)
	if e != nil || ch.GuildID != guild {
		return 0, nil, domain.Fail(domain.Unauthorized)
	}
	if ch.IsThread() {
		ch, e = a.Channel(ctx, ch.ParentID)
		if e != nil || ch.GuildID != guild {
			return 0, nil, domain.Fail(domain.Unauthorized)
		}
	}
	var g discordgo.Guild
	if e = a.JSON(ctx, "GET", "/guilds/"+guild, "guild:"+guild, nil, &g, false); e != nil {
		return 0, nil, e
	}
	var member discordgo.Member
	if e = a.JSON(ctx, "GET", "/guilds/"+guild+"/members/"+user, "guild:"+guild, nil, &member, false); e != nil {
		return 0, nil, domain.Fail(domain.Unauthorized)
	}
	var roles []*discordgo.Role
	if e = a.JSON(ctx, "GET", "/guilds/"+guild+"/roles", "guild:"+guild, nil, &roles, false); e != nil {
		return 0, nil, e
	}
	if g.OwnerID == user {
		return allPermissions, member.Roles, nil
	}
	roleSet := map[string]bool{guild: true}
	for _, r := range member.Roles {
		roleSet[r] = true
	}
	var p int64
	for _, r := range roles {
		if roleSet[r.ID] {
			p |= r.Permissions
		}
	}
	if p&discordgo.PermissionAdministrator != 0 {
		return allPermissions, member.Roles, nil
	}
	for _, o := range ch.PermissionOverwrites {
		if o.ID == guild {
			p &= ^o.Deny
			p |= o.Allow
		}
	}
	var deny, allow int64
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeRole && o.ID != guild && roleSet[o.ID] {
			deny |= o.Deny
			allow |= o.Allow
		}
	}
	p &= ^deny
	p |= allow
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeMember && o.ID == user {
			p &= ^o.Deny
			p |= o.Allow
		}
	}
	return p, member.Roles, nil
}
func (a *Adapter) CheckForum(ctx context.Context, guild string, s domain.Settings) error {
	ch, e := a.Channel(ctx, s.ForumChannelID)
	if e != nil || ch.GuildID != guild || ch.Type != discordgo.ChannelTypeGuildForum {
		return domain.Fail(domain.Configuration)
	}
	known := map[string]bool{}
	for _, tag := range ch.AvailableTags {
		known[tag.ID] = true
	}
	for _, id := range s.ForumTagIDs {
		if !known[id] {
			return domain.Fail(domain.Invalid)
		}
	}
	if ch.Flags&discordgo.ChannelFlagRequireTag != 0 && len(s.ForumTagIDs) == 0 {
		return domain.Fail(domain.Configuration)
	}
	p, _, e := a.Permissions(ctx, guild, a.BotID, ch.ID)
	if e != nil {
		return e
	}
	required := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionSendMessagesInThreads | discordgo.PermissionReadMessageHistory | discordgo.PermissionEmbedLinks | discordgo.PermissionAttachFiles)
	if p&required != required {
		return domain.Fail(domain.Unauthorized)
	}
	return nil
}
func (a *Adapter) Authorize(ctx context.Context, guild, user, channel string, admin bool, roles []string) error {
	p, actual, e := a.Permissions(ctx, guild, user, channel)
	if e != nil {
		return e
	}
	if p&discordgo.PermissionViewChannel == 0 {
		return domain.Fail(domain.Unauthorized)
	}
	if admin && p&discordgo.PermissionManageServer == 0 {
		return domain.Fail(domain.Unauthorized)
	}
	if !admin && len(roles) > 0 {
		for _, r := range actual {
			for _, allowed := range roles {
				if r == allowed {
					return nil
				}
			}
		}
		return domain.Fail(domain.Unauthorized)
	}
	return nil
}
func (a *Adapter) EnsureThread(ctx context.Context, guild, thread string) error {
	ch, e := a.Channel(ctx, thread)
	if e != nil || ch.GuildID != guild || !ch.IsThread() {
		return domain.Fail(domain.Conflict)
	}
	if ch.ThreadMetadata != nil {
		if ch.ThreadMetadata.Locked {
			return domain.Fail(domain.Conflict)
		}
		if ch.ThreadMetadata.Archived {
			return a.JSON(ctx, "PATCH", "/channels/"+thread, "channels:"+thread, map[string]bool{"archived": false}, nil, true)
		}
	}
	return nil
}
