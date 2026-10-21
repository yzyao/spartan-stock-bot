package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yzyao/spartan-stock-bot/internal/config"
	"github.com/yzyao/spartan-stock-bot/internal/monitor"
	"github.com/yzyao/spartan-stock-bot/internal/spartan"
	"github.com/yzyao/spartan-stock-bot/internal/state"
	"github.com/yzyao/spartan-stock-bot/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		slog.Error("application_stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	verify := flag.Bool("verify", false, "fetch/parse inventory once, output JSON, no Telegram or state changes")
	flag.Parse()
	cfg, err := config.Load(!*verify)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.Level}))
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpClient := spartan.HTTPClient(cfg.HTTPTimeout)
	defer httpClient.CloseIdleConnections()
	provider := &spartan.Client{HTTP: httpClient, URLs: cfg.URLs, Log: log}
	if cfg.FetchMode == "tls" {
		fetcher, err := spartan.NewTLSFetcher(spartan.TLSOptions{Profile: cfg.TLSClientProfile, ProxyURL: cfg.TLSProxyURL, Timeout: cfg.HTTPTimeout})
		if err != nil {
			return err
		}
		provider.Fetcher = fetcher
		defer fetcher.Close()
		log.Info("tls_client_initialized", "profile", cfg.TLSClientProfile, "http3", false, "proxy_enabled", cfg.TLSProxyURL != "")
	}
	if cfg.FetchMode == "browser" {
		browser := spartan.NewBrowser(ctx, spartan.BrowserOptions{Executable: cfg.BrowserExecutable, ProfileDir: cfg.BrowserProfileDir, ProxyURL: cfg.BrowserProxyURL, Timeout: cfg.BrowserTimeout, NoSandbox: cfg.BrowserNoSandbox}, log)
		provider.Fetcher = browser
		defer func() {
			if err := browser.Close(); err != nil {
				log.Warn("browser_cleanup_error", "error", err)
			}
		}()
	}
	if *verify {
		inventory, e := provider.CheckInventory(ctx)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(inventory); err != nil {
			return err
		}
		if e != nil {
			return e
		}
		if len(inventory.Issues) > 0 {
			return fmt.Errorf("inventory verification has %d warnings", len(inventory.Issues))
		}
		return nil
	}
	store := &state.Store{Path: cfg.StateFile, Log: log}
	st, err := store.Load()
	if err != nil {
		return err
	}
	// Verify the volume is writable before starting Telegram operations.
	if err = store.Save(st); err != nil {
		return err
	}
	bot := &telegram.Bot{HTTP: httpClient, BaseURL: "https://api.telegram.org", Token: cfg.Token, ChatID: cfg.ChatID, AdminIDs: cfg.AdminIDs, Log: log}
	format := telegram.Formatter{Location: cfg.Location, AffiliateID: cfg.AffiliateID}
	m := monitor.New(provider, bot, store, st, cfg.NotificationChatID, format, log)
	var wg sync.WaitGroup
	var manual atomic.Bool
	reply := func(ctx context.Context, chatID int64, text string) {
		if err := bot.ReplyTo(ctx, chatID, text); err != nil && ctx.Err() == nil {
			log.Error("telegram_reply_error", "error", err)
		}
	}
	handle := func(ctx context.Context, chatID int64, command string) {
		switch command {
		case "/help", "/start":
			reply(ctx, chatID, telegram.Help(cfg.Interval))
		case "/status":
			for _, text := range format.Status(m.Snapshot(), time.Now()) {
				reply(ctx, chatID, text)
			}
		case "/check":
			if !manual.CompareAndSwap(false, true) {
				reply(ctx, chatID, "⏳ 当前已有库存检查正在运行")
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer manual.Store(false)
				r, e := m.Check(ctx)
				if errors.Is(e, monitor.ErrBusy) {
					reply(ctx, chatID, "⏳ 当前已有库存检查正在运行")
					return
				}
				if ctx.Err() != nil {
					return
				}
				icon := "✅ 检查完成"
				if e != nil || r.Failures > 0 {
					icon = "⚠️ 检查结束，部分失败；旧数据已保留，请查看日志"
				}
				reply(ctx, chatID, fmt.Sprintf("%s\n\n共检查 %d 个分类\n发现 %d 个套餐\n当前有货 %d 个\n失败/警告 %d 项\n耗时 %.1fs", icon, r.Categories, r.Products, r.Available, r.Failures, r.Duration.Seconds()))
			}()
		default:
			reply(ctx, chatID, "未知命令，请使用 /help")
		}
	}
	wg.Add(1)
	go func() { defer wg.Done(); m.Run(ctx, cfg.Interval) }()
	log.Info("application_started", "application", "SpartanStockBot", "interval", cfg.Interval, "state_file", cfg.StateFile, "timezone", cfg.Location.String(), "fetch_mode", cfg.FetchMode)
	bot.Poll(ctx, handle)
	wg.Wait()
	// State is saved after each observation/action. Flush once more after all workers stop.
	if err = store.Save(m.Snapshot()); err != nil {
		return err
	}
	log.Info("application_stopped", "reason", "shutdown signal")
	return nil
}
