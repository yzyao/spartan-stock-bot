package spartan

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type BrowserOptions struct {
	Executable string
	ProfileDir string
	ProxyURL   string
	Timeout    time.Duration
	NoSandbox  bool
}

type browserSession interface {
	Load(context.Context, string) ([]byte, error)
	Alive() bool
	Close() error
}

// Browser owns one Chromium process. Fetches are serialized and each uses a fresh
// tab in the same profile, preserving cookies while bounding page resource growth.
type Browser struct {
	parent        context.Context
	opts          BrowserOptions
	log           *slog.Logger
	gate          chan struct{}
	session       browserSession
	sessionCancel context.CancelFunc
	closed        bool
	start         func(context.Context, BrowserOptions) (browserSession, error)
}

func NewBrowser(ctx context.Context, opts BrowserOptions, log *slog.Logger) *Browser {
	return &Browser{parent: ctx, opts: opts, log: log, gate: make(chan struct{}, 1), start: newChromiumSession}
}

func (b *Browser) Fetch(ctx context.Context, source string) ([]byte, error) {
	select {
	case b.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-b.gate }()
	if b.closed {
		return nil, fmt.Errorf("browser is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.parent.Err(); err != nil {
		return nil, err
	}
	if b.session != nil && !b.session.Alive() {
		b.log.Warn("browser_restarting", "reason", "browser process disconnected")
		if err := b.session.Close(); err != nil {
			b.log.Warn("browser_cleanup_error", "error", err)
		}
		if b.sessionCancel != nil {
			b.sessionCancel()
			b.sessionCancel = nil
		}
		b.session = nil
	}
	if b.session == nil {
		startupCtx, startupCancel := context.WithCancel(b.parent)
		stopRequest := context.AfterFunc(ctx, startupCancel)
		var err error
		b.session, err = b.start(startupCtx, b.opts)
		stopRequest()
		if err != nil {
			startupCancel()
			return nil, fmt.Errorf("start Chromium: %w", err)
		}
		b.sessionCancel = startupCancel
		if err = ctx.Err(); err != nil {
			closeErr := b.session.Close()
			startupCancel()
			b.session = nil
			b.sessionCancel = nil
			if closeErr != nil {
				b.log.Warn("browser_cleanup_error", "error", closeErr)
			}
			return nil, err
		}
		b.log.Info("browser_started", "headless", true, "profile_dir", b.opts.ProfileDir)
	}
	pageCtx, cancel := context.WithTimeout(ctx, b.opts.Timeout)
	defer cancel()
	data, err := b.session.Load(pageCtx, source)
	if err != nil {
		return nil, fmt.Errorf("browser fetch: %w", err)
	}
	return data, nil
}

func (b *Browser) Close() error {
	b.gate <- struct{}{}
	defer func() { <-b.gate }()
	b.closed = true
	if b.session == nil {
		return nil
	}
	err := b.session.Close()
	if b.sessionCancel != nil {
		b.sessionCancel()
		b.sessionCancel = nil
	}
	b.session = nil
	return err
}
