package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kzzz/atc-cf-bot/internal/catalog"
	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/discord"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/generation"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/posting"
	"github.com/kzzz/atc-cf-bot/internal/scheduler"
	"github.com/kzzz/atc-cf-bot/internal/source"
	"github.com/kzzz/atc-cf-bot/internal/store"
	"github.com/kzzz/atc-cf-bot/internal/worker"
)

func Run(parent context.Context, c config.Config, recovery bool, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	lock, e := store.AcquireSingleton(ctx, c.Database.URL)
	if e != nil {
		return e
	}
	defer lock.Close()
	db, e := store.Open(ctx, c)
	if e != nil {
		return e
	}
	defer db.Close()
	if e = db.CheckSchema(ctx); e != nil {
		return e
	}
	var monitor sync.WaitGroup
	monitor.Add(1)
	go func() { defer monitor.Done(); lock.Monitor(ctx, cancel) }()
	defer func() { cancel(); monitor.Wait() }()
	if e = db.Recover(ctx); e != nil {
		return e
	}
	adapter, e := discord.New(c.Discord.Token, db)
	if e != nil {
		return e
	}
	defer adapter.HTTP.CloseIdleConnections()
	if e = adapter.Identity(ctx); e != nil {
		return e
	}
	if e = adapter.Register(ctx, c.Discord.TestGuildID); e != nil {
		return e
	}
	client := llm.New(c.LLM, c.Limits.LLMResponseBytes)
	defer client.HTTP.CloseIdleConnections()
	coordinator := generation.New(ctx, db, c.LLM.MaxConcurrency, c.LLM.MaxQueue, c.Limits.Waiters, c.Limits.GuildWaiters, c.LLM.OperationDuration())
	defer func() { cancel(); coordinator.Close() }()
	gen := &generation.Service{Client: client, Coordinator: coordinator, BlockLimit: c.Limits.TranslationBytes}
	var gateway, initialized, degraded atomic.Bool
	health := Health(ctx, c.HealthAddress, func() bool {
		if !gateway.Load() || !initialized.Load() || ctx.Err() != nil {
			return false
		}
		probe, pc := context.WithTimeout(ctx, 2*time.Second)
		defer pc()
		return db.Pool.Ping(probe) == nil
	}, func() bool { return degraded.Load() || client.Degraded() })
	defer func() {
		clean, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = health.Shutdown(clean)
	}()
	router := &discord.Router{Root: ctx, Adapter: adapter, Store: db, Generation: gen, RecoveryMode: recovery, ManualGuildLimit: c.Limits.ManualGuildQueue, ManualGlobalLimit: c.Limits.ManualGlobalQueue, Ready: func() bool { return initialized.Load() }}
	router.Init()
	adapter.Session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) { gateway.Store(true) })
	adapter.Session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { gateway.Store(true) })
	adapter.Session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { gateway.Store(false) })
	if e = adapter.OpenGateway(ctx); e != nil {
		return domain.Fail(domain.Transient)
	}
	defer adapter.Session.Client.CloseIdleConnections()
	// Existing uncertain sends are reconciled before new jobs can begin.
	uncertain, e := db.Uncertain(ctx)
	if e != nil {
		_ = adapter.Session.Close()
		return e
	}
	for _, item := range uncertain {
		probe, pc := context.WithTimeout(ctx, 60*time.Second)
		result, e := adapter.Reconcile(probe, item.GuildID, item.Operation)
		if e == nil {
			e = db.Decide(probe, item, result, "adopt", "startup", "unique frozen payload verified")
		}
		pc()
		if e != nil {
			log.Warn("reconciliation required", "operation_id", item.Operation.ID, "code", "ambiguous")
		}
	}
	fetcher := source.NewFetcher()
	defer fetcher.Client.CloseIdleConnections()
	syncer := catalog.Syncer{Fetcher: fetcher, MaxBytes: c.Limits.CatalogBytes}
	synchronize := func() {
		failed := false
		for _, platform := range []domain.Platform{domain.AtCoder, domain.Codeforces} {
			snap, e := syncer.Fetch(ctx, platform)
			if e == nil {
				e = db.SaveCatalog(ctx, snap)
			}
			if e != nil {
				failed = true
				log.Warn("catalog synchronization unavailable", "platform", platform, "code", "source_or_database")
			}
		}
		degraded.Store(failed)
	}
	synchronize()
	guilds, e := db.Guilds(ctx)
	if e != nil {
		_ = adapter.Session.Close()
		return e
	}
	for _, guild := range guilds {
		cfg, e := db.Config(ctx, guild)
		if e != nil {
			_ = adapter.Session.Close()
			return e
		}
		if cfg.Settings.ForumChannelID != "" {
			if e = adapter.CheckForum(ctx, guild, cfg.Settings); e != nil {
				_, _ = db.Pool.Exec(ctx, "UPDATE guild_runtime SET block_reason=COALESCE(block_reason,'forum_permissions') WHERE guild_id=$1", guild)
				log.Warn("guild forum blocked", "guild_id", guild, "code", "forum_permissions")
			}
		}
	}
	post := &posting.Service{Store: db, Publisher: adapter, Source: fetcher, Generation: gen, Limits: c.Limits, RNG: domain.SecureRNG{}}
	workers := &worker.Pool{Store: db, Runner: post, Concurrency: c.Worker.Concurrency, Heartbeat: time.Duration(c.Worker.Heartbeat) * time.Second, Log: log}
	workers.Notify = func(nctx context.Context, j store.JobLease, code string) {
		cfg, e := db.Config(nctx, j.GuildID)
		if e != nil || cfg.Settings.AdminLogChannelID == "" {
			log.Warn("admin notification unavailable", "job_id", j.ID, "code", code)
			return
		}
		payload := renderNotice("發題工作停止：" + code + "。請用 /daily show 查詢，部署維護者可執行 reconcile。")
		if e = adapter.JSON(nctx, "POST", "/channels/"+cfg.Settings.AdminLogChannelID+"/messages", "channels:"+cfg.Settings.AdminLogChannelID, payload, nil, true); e != nil {
			log.Warn("admin notification failed", "job_id", j.ID, "code", "discord")
		}
	}
	var background sync.WaitGroup
	if !recovery {
		workers.Start(ctx)
		sch := &scheduler.Scheduler{Store: db, Poll: time.Duration(c.Scheduler.Poll) * time.Second, Log: log}
		background.Add(1)
		go func() { defer background.Done(); sch.Run(ctx) }()
	}
	background.Add(1)
	go func() {
		defer background.Done()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				synchronize()
			}
		}
	}()

	initialized.Store(true)

	log.Info("bot running", "recovery_mode", recovery)
	<-ctx.Done()
	initialized.Store(false)
	_ = adapter.Session.Close()
	stopped := make(chan struct{})
	go func() { workers.Wait(); background.Wait(); router.Close(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		log.Warn("shutdown deadline reached", "code", "shutdown_timeout")
	}
	clean, cc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cc()
	_ = health.Shutdown(clean)
	return nil
}
func renderNotice(text string) map[string]any {
	return map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}, "replied_user": false}}
}
func Health(ctx context.Context, address string, ready, degraded func() bool) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"live": true})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		ok := ready()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(503)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ready": ok, "degraded": degraded()})
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(_ net.Listener) context.Context { return ctx }}
	go func() { _ = server.ListenAndServe() }()
	return server
}
