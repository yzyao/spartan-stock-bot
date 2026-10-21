package spartan

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

type chromiumSession struct {
	ctx             context.Context
	cancel          context.CancelFunc
	allocatorCancel context.CancelFunc
}

func newChromiumSession(parent context.Context, opts BrowserOptions) (browserSession, error) {
	if err := os.MkdirAll(opts.ProfileDir, 0700); err != nil {
		return nil, fmt.Errorf("create browser profile: %w", err)
	}
	flags := append([]chromedp.ExecAllocatorOption(nil), chromedp.DefaultExecAllocatorOptions[:]...)
	flags = append(flags, chromedp.UserDataDir(opts.ProfileDir), chromedp.Flag("disable-dev-shm-usage", false), chromedp.Flag("disable-gpu", true), chromedp.Flag("lang", "en-US"), chromedp.NoInheritEnv, chromedp.Env(browserEnvironment()...))
	// Set this explicitly even when false: chromedp otherwise disables the sandbox automatically as root.
	flags = append(flags, chromedp.Flag("no-sandbox", opts.NoSandbox))
	if opts.Executable != "" {
		flags = append(flags, chromedp.ExecPath(opts.Executable))
	}
	if opts.ProxyURL != "" {
		flags = append(flags, chromedp.ProxyServer(opts.ProxyURL))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(parent, flags...)
	rootCtx, rootCancel := chromedp.NewContext(allocCtx)
	s := &chromiumSession{ctx: rootCtx, cancel: rootCancel, allocatorCancel: allocCancel}
	// The first chromedp call owns the browser lifetime. Use its long-lived root
	// context and a startup watchdog, so per-page timeouts never kill the process.
	watchdog := time.AfterFunc(opts.Timeout, rootCancel)
	err := chromedp.Do(rootCtx)
	watchdog.Stop()
	if err != nil {
		rootCancel()
		allocCancel()
		return nil, fmt.Errorf("launch failed (check Chromium executable, sandbox and writable profile): %w", err)
	}
	return s, nil
}

func browserEnvironment() []string {
	var env []string
	// Browser subprocesses need OS paths, not the Telegram token or application configuration.
	for _, key := range []string{"PATH", "HOME", "TMP", "TEMP", "TMPDIR", "SystemRoot", "WINDIR", "APPDATA", "LOCALAPPDATA", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func (s *chromiumSession) Alive() bool {
	if s.ctx.Err() != nil {
		return false
	}
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Browser == nil {
		return false
	}
	select {
	case <-c.Browser.LostConnection:
		return false
	default:
		return true
	}
}

func (s *chromiumSession) Close() error {
	defer s.allocatorCancel()
	defer s.cancel()
	if s.ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	err := chromedp.Cancel(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type browserPage struct {
	URL       string `json:"url"`
	HTML      string `json:"html"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Ready     bool   `json:"ready"`
	Oversized bool   `json:"oversized"`
}

// Reading the DOM does not click or attempt to solve an interactive challenge.
const pageSnapshotJS = `(() => {
 const html = document.documentElement ? document.documentElement.outerHTML : '';
 const nav = performance.getEntriesByType('navigation')[0];
 return {url:location.href,title:document.title,html:html.length <= 4194304 ? html : '',
 status:nav && nav.responseStatus ? nav.responseStatus : 0,
 ready:document.readyState === 'complete',oversized:html.length > 4194304};
})()`

func (s *chromiumSession) Load(ctx context.Context, source string) ([]byte, error) {
	tab, closeTab := chromedp.NewContext(s.ctx)
	defer closeTab()
	pageCtx, cancel := context.WithCancel(tab)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	response, err := chromedp.Run(pageCtx, chromedp.NavigateResponse(source))
	if err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}
	lastPage := browserPage{}
	for {
		p, err := chromedp.Run(pageCtx, chromedp.Evaluate[browserPage](pageSnapshotJS))
		if err != nil {
			if isChallengePage(lastPage) {
				return nil, challengeFailure(lastPage, err)
			}
			return nil, fmt.Errorf("read rendered HTML: %w", err)
		}
		if p.Status == 0 && response != nil {
			p.Status = int(response.Status)
		}
		lastPage = p
		wait, e := validateBrowserPage(p, source)
		if e != nil {
			return nil, e
		}
		if !wait {
			return []byte(p.HTML), nil
		}
		t := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			if isChallengePage(lastPage) {
				return nil, challengeFailure(lastPage, ctx.Err())
			}
			return nil, ctx.Err()
		case <-pageCtx.Done():
			t.Stop()
			if ctx.Err() != nil {
				if isChallengePage(lastPage) {
					return nil, challengeFailure(lastPage, ctx.Err())
				}
				return nil, ctx.Err()
			}
			return nil, pageCtx.Err()
		case <-t.C:
		}
	}
}

func challengeFailure(p browserPage, err error) error {
	return fmt.Errorf("Cloudflare challenge did not clear (last HTTP %d); inventory unknown: %w", p.Status, err)
}

func validateBrowserPage(p browserPage, source string) (bool, error) {
	expected, err := url.Parse(source)
	if err != nil {
		return false, fmt.Errorf("invalid inventory URL")
	}
	actual, err := url.Parse(p.URL)
	if err != nil || actual.Host != expected.Host || actual.Scheme != expected.Scheme || actual.User != nil {
		return false, fmt.Errorf("browser redirected to unexpected host or scheme; inventory unknown")
	}
	if p.Oversized || len(p.HTML) > maxPageBytes {
		return false, fmt.Errorf("oversized browser HTML; inventory unknown")
	}
	if isChallengePage(p) {
		return true, nil
	}
	if p.Status < 200 || p.Status >= 300 {
		return false, fmt.Errorf("browser HTTP %d; inventory unknown", p.Status)
	}
	if !p.Ready {
		return true, nil
	}
	if strings.TrimSpace(p.HTML) == "" {
		return false, fmt.Errorf("empty browser HTML; inventory unknown")
	}
	return false, nil
}

func isChallengePage(p browserPage) bool {
	title := strings.ToLower(strings.TrimSpace(p.Title))
	if strings.Contains(title, "just a moment") || strings.Contains(title, "attention required! | cloudflare") {
		return true
	}
	html := strings.ToLower(p.HTML)
	return strings.Contains(html, "_cf_chl_opt") || strings.Contains(html, `id="challenge-running"`) || strings.Contains(html, `id="challenge-stage"`)
}
