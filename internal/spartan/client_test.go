package spartan

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientPartialFailures(t *testing.T) {
	for _, bad := range []string{"500", "403", "empty", "layout", "timeout", "redirect", "oversize"} {
		t.Run(bad, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.UserAgent() != UserAgent {
					t.Errorf("User-Agent %q", r.UserAgent())
				}
				if r.URL.Path == "/good" {
					io.WriteString(w, card("3 Available"))
					return
				}
				switch bad {
				case "500":
					w.WriteHeader(500)
				case "403":
					w.WriteHeader(403)
				case "empty":
					return
				case "layout":
					io.WriteString(w, "<h1>Challenge</h1>")
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				case "redirect":
					http.Redirect(w, r, "https://unexpected.invalid/", http.StatusFound)
				case "oversize":
					io.WriteString(w, strings.Repeat("x", maxPageBytes+1))
				}
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 100 * time.Millisecond
			client.CheckRedirect = HTTPClient(time.Second).CheckRedirect
			c := &Client{HTTP: client, URLs: []string{server.URL + "/bad", server.URL + "/good"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			result, err := c.CheckInventory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.Categories != 1 || len(result.Products) != 1 || result.Products[0].AvailableQuantity != 3 || len(result.Issues) != 1 {
				t.Fatalf("partial failure became zero or lost healthy page: %+v", result)
			}
			c.URLs = c.URLs[:1]
			result, err = c.CheckInventory(context.Background())
			if err == nil || len(result.Products) != 0 {
				t.Fatalf("all failed: %+v %v", result, err)
			}
		})
	}
}

func TestClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{HTTP: HTTPClient(time.Second), URLs: []string{source}, Log: slog.Default()}
	if _, err := c.CheckInventory(ctx); err != context.Canceled {
		t.Fatalf("cancellation %v", err)
	}
}
