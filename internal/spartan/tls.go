package spartan

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

type TLSOptions struct {
	Profile  string
	ProxyURL string
	Timeout  time.Duration
}

// TLSFetcher reuses a browser-fingerprinted TLS/HTTP client and an in-memory
// cookie jar. It does not execute JavaScript or solve interactive challenges.
type TLSFetcher struct {
	client  tlsclient.HttpClient
	headers http.Header
	timeout time.Duration
}

func NewTLSFetcher(opts TLSOptions) (*TLSFetcher, error) {
	return newTLSFetcher(opts, nil)
}

func newTLSFetcher(opts TLSOptions, roots *x509.CertPool) (*TLSFetcher, error) {
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("TLS request timeout must be positive")
	}
	profile := profiles.Chrome_150
	headers := http.Header{
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
		"accept-language":           {"en-US,en;q=0.9"},
		"sec-fetch-dest":            {"document"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-site":            {"none"},
		"sec-fetch-user":            {"?1"},
		"upgrade-insecure-requests": {"1"},
		http.HeaderOrderKey:         {"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests", "user-agent", "accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest", "accept-encoding", "accept-language", "cookie"},
	}
	switch opts.Profile {
	case "chrome_150":
		headers["user-agent"] = []string{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"}
		headers["sec-ch-ua"] = []string{`"Chromium";v="150", "Google Chrome";v="150", "Not_A Brand";v="99"`}
		headers["sec-ch-ua-mobile"] = []string{"?0"}
		headers["sec-ch-ua-platform"] = []string{`"Linux"`}
	case "firefox_148":
		profile = profiles.Firefox_148
		headers["user-agent"] = []string{"Mozilla/5.0 (X11; Linux x86_64; rv:148.0) Gecko/20100101 Firefox/148.0"}
		headers[http.HeaderOrderKey] = []string{"user-agent", "accept", "accept-language", "accept-encoding", "upgrade-insecure-requests", "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "sec-fetch-user", "cookie"}
	default:
		return nil, fmt.Errorf("unsupported TLS client profile")
	}
	idleTimeout := 90 * time.Second
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profile),
		tlsclient.WithTimeoutMilliseconds(int(opts.Timeout.Milliseconds())),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithNotFollowRedirects(),
		// HTTP/2 avoids QUIC/UDP requirements on small VPS deployments and proxies.
		tlsclient.WithDisableHttp3(),
		tlsclient.WithTransportOptions(&tlsclient.TransportOptions{
			RootCAs: roots, MaxIdleConns: 16, MaxIdleConnsPerHost: 4,
			IdleConnTimeout: &idleTimeout, MaxResponseHeaderBytes: 64 << 10,
		}),
	}
	if opts.Profile == "chrome_150" {
		options = append(options, tlsclient.WithRandomTLSExtensionOrder())
	}
	if opts.ProxyURL != "" {
		options = append(options, tlsclient.WithProxyUrl(opts.ProxyURL))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("initialize TLS client: %w", err)
	}
	return &TLSFetcher{client: client, headers: headers, timeout: opts.Timeout}, nil
}

func (f *TLSFetcher) Close() { f.client.CloseIdleConnections() }

func (f *TLSFetcher) Fetch(ctx context.Context, source string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	origin, err := url.Parse(source)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil {
		return nil, fmt.Errorf("TLS inventory URL must use HTTPS without credentials")
	}
	current := origin
	for redirects := 0; ; redirects++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create TLS request: %w", err)
		}
		req.Header = f.headers.Clone()
		resp, err := f.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("TLS fetch: %w", err)
		}
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := resp.Header.Get("Location")
			closeErr := resp.Body.Close()
			if closeErr != nil {
				return nil, fmt.Errorf("close TLS redirect body: %w", closeErr)
			}
			if redirects >= 5 {
				return nil, fmt.Errorf("too many TLS redirects; inventory unknown")
			}
			next, err := current.Parse(location)
			if err != nil || location == "" || next.Host != origin.Host || next.Scheme != "https" || next.User != nil {
				return nil, fmt.Errorf("unexpected TLS redirect host or scheme; inventory unknown")
			}
			current = next
			continue
		default:
			return readTLSPage(resp)
		}
	}
}

func readTLSPage(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("cf-mitigated")), "challenge") {
		return nil, fmt.Errorf("TLS HTTP %d: Cloudflare challenge requires JavaScript or interaction; inventory unknown", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TLS HTTP %d; inventory unknown", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read TLS HTML: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 || len(data) > maxPageBytes {
		return nil, fmt.Errorf("empty or oversized TLS HTML; inventory unknown")
	}
	if isChallengePage(browserPage{HTML: string(data)}) {
		return nil, fmt.Errorf("TLS response is a Cloudflare challenge; inventory unknown")
	}
	return data, nil
}
