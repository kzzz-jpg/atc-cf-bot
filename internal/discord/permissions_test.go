package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

const testGuild = "111111111"
const testForum = "222222222"
const testUser = "333333333"
const testRole = "444444444"

func permissionAdapter(t *testing.T, forum *discordgo.Channel, member func() []string, rolePermissions int64) (*Adapter, *atomic.Int32) {
	t.Helper()
	reads := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/channels/"+testForum:
			_ = json.NewEncoder(w).Encode(forum)
		case r.URL.Path == "/channels/555555555":
			_ = json.NewEncoder(w).Encode(discordgo.Channel{ID: "555555555", GuildID: testGuild, ParentID: testForum, Type: discordgo.ChannelTypeGuildPublicThread})
		case strings.Contains(r.URL.Path, "/members/"):
			reads.Add(1)
			_ = json.NewEncoder(w).Encode(discordgo.Member{User: &discordgo.User{ID: testUser}, Roles: member()})
		case strings.HasSuffix(r.URL.Path, "/roles"):
			_ = json.NewEncoder(w).Encode([]*discordgo.Role{{ID: testGuild, Permissions: discordgo.PermissionViewChannel}, {ID: testRole, Permissions: rolePermissions}})
		case r.URL.Path == "/guilds/"+testGuild:
			_ = json.NewEncoder(w).Encode(discordgo.Guild{ID: testGuild, OwnerID: "999999999"})
		default:
			t.Errorf("unexpected permissions request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	a, e := New("secret", nil)
	if e != nil {
		t.Fatal(e)
	}
	a.BaseURL = server.URL
	a.BotID = testUser
	return a, reads
}

func TestAuthorizeRefetchesRevokedRoles(t *testing.T) {
	var present atomic.Bool
	present.Store(true)
	forum := &discordgo.Channel{ID: testForum, GuildID: testGuild, Type: discordgo.ChannelTypeGuildForum}
	a, reads := permissionAdapter(t, forum, func() []string {
		if present.Load() {
			return []string{testRole}
		}
		return nil
	}, discordgo.PermissionManageServer)
	if e := a.Authorize(context.Background(), testGuild, testUser, testForum, false, []string{testRole}); e != nil {
		t.Fatal(e)
	}
	if e := a.Authorize(context.Background(), testGuild, testUser, testForum, true, nil); e != nil {
		t.Fatal(e)
	}
	present.Store(false)
	if e := a.Authorize(context.Background(), testGuild, testUser, testForum, false, []string{testRole}); e == nil {
		t.Fatal("revoked manual role remained authorized")
	}
	if e := a.Authorize(context.Background(), testGuild, testUser, testForum, true, nil); e == nil {
		t.Fatal("revoked manage server remained authorized")
	}
	if reads.Load() != 4 {
		t.Fatal("permissions reused stale member")
	}
}

func TestPermissionsUseParentAndOverwritePrecedence(t *testing.T) {
	forum := &discordgo.Channel{ID: testForum, GuildID: testGuild, Type: discordgo.ChannelTypeGuildForum, PermissionOverwrites: []*discordgo.PermissionOverwrite{
		{ID: testGuild, Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionAttachFiles},
		{ID: testRole, Type: discordgo.PermissionOverwriteTypeRole, Allow: discordgo.PermissionAttachFiles},
		{ID: testUser, Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionAttachFiles},
	}}
	a, _ := permissionAdapter(t, forum, func() []string { return []string{testRole} }, discordgo.PermissionAttachFiles)
	p, _, e := a.Permissions(context.Background(), testGuild, testUser, "555555555")
	if e != nil || p&discordgo.PermissionAttachFiles != 0 || p&discordgo.PermissionViewChannel == 0 {
		t.Fatal("parent/member overwrite precedence incorrect", e, p)
	}
	if _, _, e = a.Permissions(context.Background(), "777777777", testUser, testForum); e == nil {
		t.Fatal("cross guild permissions accepted")
	}
}

func TestForumChecksTagsAndCurrentBotPermissions(t *testing.T) {
	required := int64(discordgo.PermissionSendMessages | discordgo.PermissionSendMessagesInThreads | discordgo.PermissionReadMessageHistory | discordgo.PermissionEmbedLinks | discordgo.PermissionAttachFiles)
	for _, tc := range []struct {
		name string
		tags []string
		deny int64
		ok   bool
	}{
		{"required tag absent", nil, 0, false},
		{"unknown tag", []string{"888888888"}, 0, false},
		{"configured tag", []string{"666666666"}, 0, true},
		{"attach revoked", []string{"666666666"}, discordgo.PermissionAttachFiles, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forum := &discordgo.Channel{ID: testForum, GuildID: testGuild, Type: discordgo.ChannelTypeGuildForum, Flags: discordgo.ChannelFlagRequireTag, AvailableTags: []discordgo.ForumTag{{ID: "666666666"}}, PermissionOverwrites: []*discordgo.PermissionOverwrite{{ID: testUser, Type: discordgo.PermissionOverwriteTypeMember, Deny: tc.deny}}}
			a, _ := permissionAdapter(t, forum, func() []string { return []string{testRole} }, required)
			cfg := domain.DefaultSettings()
			cfg.ForumChannelID = testForum
			cfg.ForumTagIDs = tc.tags
			if e := a.CheckForum(context.Background(), testGuild, cfg); (e == nil) != tc.ok {
				t.Fatalf("unexpected forum check: %v", e)
			}
		})
	}
}

func TestForumOwnerAndAdministratorHaveThreadPermissions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		user        string
		permissions int64
	}{
		{"owner", "999999999", 0},
		{"administrator", testUser, discordgo.PermissionAdministrator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forum := &discordgo.Channel{
				ID: testForum, GuildID: testGuild, Type: discordgo.ChannelTypeGuildForum,
				PermissionOverwrites: []*discordgo.PermissionOverwrite{
					{ID: testGuild, Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel | discordgo.PermissionSendMessagesInThreads},
					{ID: tc.user, Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionAttachFiles},
				},
			}
			a, _ := permissionAdapter(t, forum, func() []string { return []string{testRole} }, tc.permissions)
			a.BotID = tc.user
			cfg := domain.DefaultSettings()
			cfg.ForumChannelID = testForum
			if e := a.CheckForum(context.Background(), testGuild, cfg); e != nil {
				t.Fatalf("forum permissions rejected %s: %v", tc.name, e)
			}
			if e := a.Authorize(context.Background(), testGuild, tc.user, testForum, true, nil); e != nil {
				t.Fatalf("management permissions rejected %s: %v", tc.name, e)
			}
		})
	}
}
