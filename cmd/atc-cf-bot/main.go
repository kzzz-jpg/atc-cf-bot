package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/app"
	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/discord"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

func main() {
	if e := execute(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "操作失敗：", safeCLI(e))
		os.Exit(1)
	}
}
func safeCLI(e error) string {
	if v, ok := e.(*domain.Error); ok {
		return string(v.Code)
	}
	return "請檢查設定、資料庫／服務狀態與 schema 版本；敏感底層錯誤已省略"
}
func execute(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: atc-cf-bot run|migrate|check-config|reconcile")
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	path := fs.String("config", "", "YAML configuration path")
	connect := fs.Bool("connect", false, "read-only connection checks")
	recovery := fs.Bool("recovery-mode", false, "disable new public work after backup restore")
	operation := fs.String("operation", "", "operation UUID for a maintenance decision")
	adopt := fs.String("adopt", "", "verify and adopt an existing Discord ID")
	notDelivered := fs.Bool("confirm-not-delivered", false, "explicitly authorize resending a verified absent operation")
	actor := fs.String("actor", "", "maintenance operator label")
	reason := fs.String("reason", "", "audit reason")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return domain.Fail(domain.Invalid)
	}
	if command != "run" && command != "migrate" && command != "check-config" && command != "reconcile" {
		return domain.Fail(domain.Invalid)
	}
	cfg, e := config.Load(*path, os.LookupEnv, command == "migrate")
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return domain.Fail(domain.Configuration)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if command == "run" {
		return app.Run(ctx, cfg, *recovery, slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	}
	if command == "check-config" && !*connect {
		fmt.Println("設定有效（未呼叫外部 API）。")
		return nil
	}
	if command == "migrate" {
		lock, e := store.AcquireSingleton(ctx, cfg.Database.URL)
		if e != nil {
			return e
		}
		defer lock.Close()
		defer watchSingleton(ctx, cancel, lock)()
		if e = store.Migrate(ctx, cfg.Database.URL); e == nil {
			fmt.Println("Migration 完成。")
		}
		return e
	}
	writable := command == "reconcile" && (*adopt != "" || *notDelivered)
	if writable {
		if *operation == "" || *actor == "" || *reason == "" || (*adopt != "" && *notDelivered) {
			return domain.Fail(domain.Invalid)
		}
		lock, e := store.AcquireSingleton(ctx, cfg.Database.URL)
		if e != nil {
			return e
		}
		defer lock.Close()
		defer watchSingleton(ctx, cancel, lock)()
	}
	db, e := store.Open(ctx, cfg)
	if e != nil {
		return e
	}
	defer db.Close()
	if e = db.CheckSchema(ctx); e != nil {
		return e
	}
	adapter, e := discord.New(cfg.Discord.Token, db)
	if e != nil {
		return e
	}
	if e = adapter.Identity(ctx); e != nil {
		return e
	}
	if command == "check-config" {
		guilds, e := db.Guilds(ctx)
		if e != nil {
			return e
		}
		for _, guild := range guilds {
			g, e := db.Config(ctx, guild)
			if e != nil {
				return e
			}
			if g.Settings.ForumChannelID != "" {
				if e = adapter.CheckForum(ctx, guild, g.Settings); e != nil {
					return e
				}
			}
		}
		client := llm.New(cfg.LLM, cfg.Limits.LLMResponseBytes)
		defer client.HTTP.CloseIdleConnections()
		if e = client.CheckConnectivity(ctx); e != nil {
			return e
		}
		fmt.Println("資料庫、Discord 身分、討論區與 LLM 模型清單檢查完成；未建立貼文或生成內容。")
		return nil
	}
	items, e := db.Uncertain(ctx)
	if e != nil {
		return e
	}
	matched := false
	for _, item := range items {
		if *operation != "" && item.Operation.ID != *operation {
			continue
		}
		matched = true
		probe, pc := context.WithTimeout(ctx, 60*time.Second)
		if writable {
			var result store.DiscordResult
			decision := "not_delivered"
			if *adopt != "" {
				decision = "adopt"
				result, e = adapter.VerifyAdoption(probe, item.GuildID, item.Operation, *adopt)
			} else {
				_, check := adapter.Reconcile(probe, item.GuildID, item.Operation)
				if check == nil {
					e = domain.Fail(domain.Conflict)
				}
			}
			if e == nil {
				e = db.Decide(probe, item, result, decision, *actor, *reason)
			}
			pc()
			if e != nil {
				return e
			}
			fmt.Printf("已保存決定：%s %s\n", item.Operation.ID, decision)
		} else {
			result, check := adapter.Reconcile(probe, item.GuildID, item.Operation)
			pc()
			status := "needs_reconciliation"
			if check == nil {
				status = "unique_match:" + result.ID
			}
			fmt.Printf("guild=%s operation=%s status=%s\n", item.GuildID, item.Operation.ID, status)
		}
	}
	if *operation != "" && !matched {
		return domain.Fail(domain.Invalid)
	}
	return nil
}

func watchSingleton(ctx context.Context, cancel context.CancelFunc, lock *store.Singleton) func() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); lock.Monitor(ctx, cancel) }()
	return func() { cancel(); wg.Wait() }
}
