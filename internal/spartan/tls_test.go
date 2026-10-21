package spartan

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testTLSFetcher(t *testing.T, server *httptest.Server, profile string, timeout time.Duration) *TLSFetcher {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	f, err := newTLSFetcher(TLSOptions{Profile: profile, Timeout: timeout}, roots)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

func TestTLSProfilesCookiesAndRedirects(t *testing.T) {
	for _, profile := range []string{"chrome_150", "firefox_148"} {
		t.Run(profile, func(t *testing.T) {
			var mu sync.Mutex
			var hasGREASE bool
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("expected HTTP/2, got %s", r.Proto)
				}
				if profile == "chrome_150" && (!strings.Contains(r.UserAgent(), "Chrome/150.") || !strings.Contains(r.Header.Get("sec-ch-ua"), `v="150"`)) {
					t.Errorf("Chrome headers mismatch: %v", r.Header)
				}
				if profile == "firefox_148" && (!strings.Contains(r.UserAgent(), "Firefox/148.") || r.Header.Get("sec-ch-ua") != "") {
					t.Errorf("Firefox headers mismatch: %v", r.Header)
				}
				if r.URL.Path == "/set" {
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "retained", Path: "/", Secure: true})
					http.Redirect(w, r, "/good", http.StatusFound)
					return
				}
				cookie, err := r.Cookie("session")
				if err != nil || cookie.Value != "retained" {
					t.Error("session cookie lost across redirect or requests")
				}
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("Content-Encoding", "gzip")
				gz := gzip.NewWriter(w)
				if _, err := io.WriteString(gz, card("3 Available")); err != nil {
					t.Error(err)
				}
				if err := gz.Close(); err != nil {
					t.Error(err)
				}
			}))
			server.EnableHTTP2 = true
			server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				mu.Lock()
				defer mu.Unlock()
				for _, cipher := range hello.CipherSuites {
					if cipher&0x0f0f == 0x0a0a {
						hasGREASE = true
					}
				}
				return nil, nil
			}}
			server.StartTLS()
			defer server.Close()
			f := testTLSFetcher(t, server, profile, 3*time.Second)
			for _, path := range []string{"/set", "/good"} {
				data, err := f.Fetch(context.Background(), server.URL+path)
				if err != nil {
					t.Fatal(err)
				}
				products, _, err := Parse(strings.NewReader(string(data)), server.URL+path)
				if err != nil || len(products) != 1 || products[0].AvailableQuantity != 3 {
					t.Fatalf("TLS response not decompressed/parsed: %s %v", data, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if profile == "chrome_150" && !hasGREASE {
				t.Fatal("Chrome ClientHello did not contain GREASE; fingerprint was not used")
			}
		})
	}
}

func TestTLSFailuresRemainUnknown(t *testing.T) {
	var forbiddenTargetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forbiddenTargetCalls.Add(1)
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good":
			io.WriteString(w, card("3 Available"))
		case "/zero":
			io.WriteString(w, card("0 Available"))
		case "/403":
			w.WriteHeader(http.StatusForbidden)
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/challenge-header":
			w.Header().Set("cf-mitigated", "challenge")
			io.WriteString(w, card("0 Available"))
		case "/challenge-html":
			io.WriteString(w, `<script>window._cf_chl_opt={}</script>`)
		case "/layout":
			io.WriteString(w, "<h1>Unexpected layout</h1>")
		case "/empty":
		case "/oversize":
			io.WriteString(w, strings.Repeat("x", maxPageBytes+1))
		case "/timeout":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/cross-host":
			http.Redirect(w, r, target.URL, http.StatusFound)
		case "/downgrade":
			http.Redirect(w, r, "http://"+r.Host, http.StatusFound)
		case "/credentials":
			http.Redirect(w, r, "https://secret:pass@"+r.Host, http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer server.Close()
	f := testTLSFetcher(t, server, "chrome_150", 300*time.Millisecond)
	for _, path := range []string{"403", "500", "challenge-header", "challenge-html", "layout", "empty", "oversize", "timeout", "cross-host", "downgrade", "credentials", "loop"} {
		t.Run(path, func(t *testing.T) {
			c := &Client{Fetcher: f, URLs: []string{server.URL + "/" + path, server.URL + "/good"}, Log: browserLog()}
			result, err := c.CheckInventory(context.Background())
			if err != nil || result.Categories != 1 || len(result.Products) != 1 || result.Products[0].AvailableQuantity != 3 || len(result.Issues) != 1 {
				t.Fatalf("unknown TLS inventory became zero or lost healthy page: %+v %v", result, err)
			}
			if strings.Contains(result.Issues[0].Error, "secret:pass") {
				t.Fatal("redirect credentials leaked")
			}
			c.URLs = c.URLs[:1]
			result, err = c.CheckInventory(context.Background())
			if err == nil || len(result.Products) != 0 {
				t.Fatalf("failed TLS page became inventory: %+v %v", result, err)
			}
		})
	}
	if forbiddenTargetCalls.Load() != 0 {
		t.Fatal("TLS client followed cross-host redirect")
	}
	data, err := f.Fetch(context.Background(), server.URL+"/zero")
	if err != nil {
		t.Fatal(err)
	}
	products, _, err := Parse(strings.NewReader(string(data)), server.URL+"/zero")
	if err != nil || len(products) != 1 || products[0].AvailableQuantity != 0 {
		t.Fatalf("real zero inventory rejected: %+v %v", products, err)
	}
}

func TestTLSCertificateAndCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, card("1 Available"))
	}))
	defer server.Close()
	f, err := NewTLSFetcher(TLSOptions{Profile: "chrome_150", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Fetch(context.Background(), server.URL); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}
	trusted := testTLSFetcher(t, server, "chrome_150", time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := trusted.Fetch(ctx, server.URL); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := trusted.Fetch(context.Background(), "http://example.com"); err == nil {
		t.Fatal("non-HTTPS URL accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("request sent after cancellation or certificate failure")
	}
}
