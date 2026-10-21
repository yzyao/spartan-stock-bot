package spartan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in, local-only test. No real Spartan/Telegram traffic and no interaction with a CAPTCHA.
func TestBrowserIntegration(t *testing.T) {
	if os.Getenv("SPARTAN_BROWSER_TESTS") != "1" {
		t.Skip("set SPARTAN_BROWSER_TESTS=1 with Chrome/Chromium installed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/render":
			http.SetCookie(w, &http.Cookie{Name: "inventory_session", Value: "kept", Path: "/", MaxAge: 3600})
			fmt.Fprintf(w, `<html><head><title>Just a moment...</title></head><body><script>setTimeout(() => {document.title='Products';document.body.innerHTML=%s;},150);</script></body></html>`, strconv.Quote(card("3 Available")))
		case "/cookie":
			cookie, err := r.Cookie("inventory_session")
			if err != nil || cookie.Value != "kept" {
				w.WriteHeader(403)
				io.WriteString(w, "missing cookie")
				return
			}
			io.WriteString(w, card("1 Available"))
		case "/forever":
			w.WriteHeader(403)
			io.WriteString(w, `<html><head><title>Just a moment...</title></head><body>Challenge remains</body></html>`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	noSandbox, err := strconv.ParseBool(envOr("BROWSER_NO_SANDBOX", "false"))
	if err != nil {
		t.Fatal(err)
	}
	opts := BrowserOptions{Executable: os.Getenv("BROWSER_EXECUTABLE"), ProfileDir: t.TempDir() + "/profile", Timeout: 15 * time.Second, NoSandbox: noSandbox}
	b := NewBrowser(context.Background(), opts, browserLog())
	defer b.Close()
	data, err := b.Fetch(context.Background(), server.URL+"/render")
	if err != nil {
		t.Fatal(err)
	}
	products, _, err := Parse(strings.NewReader(string(data)), source)
	if err != nil || len(products) != 1 || products[0].AvailableQuantity != 3 {
		t.Fatalf("JS-rendered HTML not parsed: %+v %v", products, err)
	}
	session := b.session
	if _, err = b.Fetch(context.Background(), server.URL+"/cookie"); err != nil {
		t.Fatalf("cookies not shared between tabs: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	if _, err = b.Fetch(ctx, server.URL+"/forever"); err == nil {
		t.Fatal("challenge became valid inventory")
	}
	if _, err = b.Fetch(context.Background(), server.URL+"/cookie"); err != nil {
		t.Fatalf("page timeout damaged browser: %v", err)
	}
	if session != b.session {
		t.Fatal("healthy browser was restarted per page")
	}
	if err = b.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	t.Run("persistent_profile_cookie", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("deployment cookie persistence requires Linux Chromium; managed Windows may lack DPAPI encryption")
		}
		b = NewBrowser(context.Background(), opts, browserLog())
		defer b.Close()
		if _, err = b.Fetch(context.Background(), server.URL+"/cookie"); err != nil {
			t.Fatalf("cookies lost after browser restart: %v", err)
		}
	})
}

func envOr(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func TestBrowserIntegrationTemporaryProfileIsolation(t *testing.T) {
	if os.Getenv("SPARTAN_BROWSER_TESTS") != "1" {
		t.Skip("set SPARTAN_BROWSER_TESTS=1 with Chrome/Chromium installed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "profile_session", Value: "isolated", Path: "/"})
		}
		cookie, err := r.Cookie("profile_session")
		if err == nil && cookie.Value == "isolated" {
			io.WriteString(w, "private session")
		} else {
			io.WriteString(w, "fresh session")
		}
	}))
	defer server.Close()
	noSandbox, err := strconv.ParseBool(envOr("BROWSER_NO_SANDBOX", "false"))
	if err != nil {
		t.Fatal(err)
	}
	opts := BrowserOptions{Executable: os.Getenv("BROWSER_EXECUTABLE"), Timeout: 15 * time.Second, NoSandbox: noSandbox}
	first := NewBrowser(context.Background(), opts, browserLog())
	defer first.Close()
	if _, err := first.Fetch(context.Background(), server.URL+"/set"); err != nil {
		t.Fatal(err)
	}
	data, err := first.Fetch(context.Background(), server.URL+"/read")
	if err != nil || !strings.Contains(string(data), "private session") {
		t.Fatalf("temporary profile did not retain session cookies: %s %v", data, err)
	}
	// A second live process must start without sharing the first process's profile or cookies.
	second := NewBrowser(context.Background(), opts, browserLog())
	defer second.Close()
	data, err = second.Fetch(context.Background(), server.URL+"/read")
	if err != nil || !strings.Contains(string(data), "fresh session") {
		t.Fatalf("temporary profiles were not isolated: %s %v", data, err)
	}
	data, err = first.Fetch(context.Background(), server.URL+"/read")
	if err != nil || !strings.Contains(string(data), "private session") {
		t.Fatalf("second browser damaged first session: %s %v", data, err)
	}
}
