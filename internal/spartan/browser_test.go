package spartan

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeBrowserSession struct {
	alive  bool
	closed int
	load   func(context.Context, string) ([]byte, error)
}

func (s *fakeBrowserSession) Load(ctx context.Context, url string) ([]byte, error) {
	return s.load(ctx, url)
}
func (s *fakeBrowserSession) Alive() bool  { return s.alive }
func (s *fakeBrowserSession) Close() error { s.closed++; return nil }
func browserLog() *slog.Logger             { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBrowserReuseAndCrashRecovery(t *testing.T) {
	b := NewBrowser(context.Background(), BrowserOptions{Timeout: time.Second}, browserLog())
	defer b.Close()
	starts := 0
	var sessions []*fakeBrowserSession
	b.start = func(context.Context, BrowserOptions) (browserSession, error) {
		starts++
		s := &fakeBrowserSession{alive: true, load: func(context.Context, string) ([]byte, error) { return []byte(card("3 Available")), nil }}
		sessions = append(sessions, s)
		return s, nil
	}
	for range 3 {
		if _, err := b.Fetch(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 {
		t.Fatalf("browser restarted per page: %d", starts)
	}
	sessions[0].alive = false
	if _, err := b.Fetch(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if starts != 2 || sessions[0].closed != 1 {
		t.Fatal("disconnected process not replaced")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Fetch(context.Background(), source); err == nil {
		t.Fatal("closed browser was restarted")
	}
}

func TestBrowserTimeoutDoesNotReplaceHealthySession(t *testing.T) {
	b := NewBrowser(context.Background(), BrowserOptions{Timeout: 10 * time.Millisecond}, browserLog())
	defer b.Close()
	starts, calls := 0, 0
	b.start = func(context.Context, BrowserOptions) (browserSession, error) {
		starts++
		return &fakeBrowserSession{alive: true, load: func(ctx context.Context, _ string) ([]byte, error) {
			calls++
			if calls == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []byte(card("1 Available")), nil
		}}, nil
	}
	if _, err := b.Fetch(context.Background(), source); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout %v", err)
	}
	if _, err := b.Fetch(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatal("page timeout killed healthy browser")
	}
}

func TestBrowserSerializesFetchesAndWaitingCancellation(t *testing.T) {
	b := NewBrowser(context.Background(), BrowserOptions{Timeout: time.Second}, browserLog())
	defer b.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	b.start = func(context.Context, BrowserOptions) (browserSession, error) {
		return &fakeBrowserSession{alive: true, load: func(context.Context, string) ([]byte, error) { close(entered); <-release; return []byte("html"), nil }}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := b.Fetch(context.Background(), source); done <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := b.Fetch(ctx, source); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent fetch/cancellation: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBrowserStartFailureRetriesNextFetch(t *testing.T) {
	b := NewBrowser(context.Background(), BrowserOptions{Timeout: time.Second}, browserLog())
	defer b.Close()
	starts := 0
	b.start = func(context.Context, BrowserOptions) (browserSession, error) {
		starts++
		return nil, errors.New("executable missing")
	}
	for range 2 {
		if _, err := b.Fetch(context.Background(), source); err == nil {
			t.Fatal("startup error lost")
		}
	}
	if starts != 2 {
		t.Fatal("startup failure not retried")
	}
}

func TestBrowserStartupHonorsRequestCancellation(t *testing.T) {
	b := NewBrowser(context.Background(), BrowserOptions{Timeout: time.Minute}, browserLog())
	defer b.Close()
	b.start = func(ctx context.Context, _ BrowserOptions) (browserSession, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := b.Fetch(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup did not cancel: %v", err)
	}
}

func TestBrowserPageValidation(t *testing.T) {
	valid := browserPage{URL: source, HTML: card("0 Available"), Title: "Products", Status: 200, Ready: true}
	for _, tc := range []struct {
		name       string
		change     func(*browserPage)
		wait, fail bool
	}{
		{"valid zero", func(*browserPage) {}, false, false},
		{"Cloudflare 403", func(p *browserPage) {
			p.Status = 403
			p.Title = "Just a moment..."
			p.HTML = "<script>window._cf_chl_opt={}</script>"
		}, true, false},
		{"Cloudflare 200", func(p *browserPage) { p.HTML = `<div id="challenge-stage"></div>` }, true, false},
		{"still loading", func(p *browserPage) { p.Ready = false }, true, false},
		{"HTTP failure", func(p *browserPage) { p.Status = 503 }, false, true},
		{"forbidden", func(p *browserPage) { p.Status = 403 }, false, true},
		{"empty", func(p *browserPage) { p.HTML = "" }, false, true},
		{"too large", func(p *browserPage) { p.Oversized = true }, false, true},
		{"offsite redirect", func(p *browserPage) { p.URL = "https://unexpected.example/store" }, false, true},
		{"TLS downgrade", func(p *browserPage) { p.URL = strings.Replace(source, "https:", "http:", 1) }, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			wait, err := validateBrowserPage(p, source)
			if wait != tc.wait || (err != nil) != tc.fail {
				t.Fatalf("wait=%t err=%v", wait, err)
			}
		})
	}
}

type fetchFunc func(context.Context, string) ([]byte, error)

func (f fetchFunc) Fetch(ctx context.Context, url string) ([]byte, error) { return f(ctx, url) }
func TestBrowserFetcherFailureRetainsHealthyPages(t *testing.T) {
	c := &Client{URLs: []string{source + "/failed", source}, Log: browserLog(), Fetcher: fetchFunc(func(_ context.Context, url string) ([]byte, error) {
		if strings.HasSuffix(url, "/failed") {
			return nil, errors.New("Cloudflare challenge timeout")
		}
		return []byte(card("3 Available")), nil
	})}
	result, err := c.CheckInventory(context.Background())
	if err != nil || len(result.Products) != 1 || result.Products[0].AvailableQuantity != 3 || len(result.Issues) != 1 {
		t.Fatalf("browser failure became stock zero: %+v %v", result, err)
	}
}

func TestBrowserEnvironmentExcludesSecrets(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:SECRET")
	t.Setenv("TELEGRAM_ADMIN_IDS", "42")
	t.Setenv("PATH", os.Getenv("PATH"))
	for _, value := range browserEnvironment() {
		if strings.Contains(value, "SECRET") || strings.HasPrefix(value, "TELEGRAM_") {
			t.Fatalf("browser inherited secret %q", value)
		}
	}
}
