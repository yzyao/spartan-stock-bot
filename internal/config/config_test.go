package config

import (
	"strings"
	"testing"
	"time"
)

func envDefaults(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CHECK_INTERVAL_SECONDS", "HTTP_TIMEOUT_SECONDS", "STATE_FILE", "LOG_LEVEL", "TZ", "SPARTAN_URLS", "TELEGRAM_CHANNEL_ID", "TELEGRAM_ADMIN_IDS", "SPARTAN_AFFILIATE_ID", "FETCH_MODE", "TLS_CLIENT_PROFILE", "TLS_PROXY_URL", "BROWSER_EXECUTABLE", "BROWSER_PROFILE_DIR", "BROWSER_PROXY_URL", "BROWSER_TIMEOUT_SECONDS", "BROWSER_NO_SANDBOX"} {
		t.Setenv(k, "")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:FAKE")
	t.Setenv("TELEGRAM_CHAT_ID", "42")
}

func TestTLSConfiguration(t *testing.T) {
	envDefaults(t)
	t.Setenv("FETCH_MODE", "tls")
	c, err := Load(false)
	if err != nil || c.FetchMode != "tls" || c.TLSClientProfile != "chrome_150" || c.TLSProxyURL != "" {
		t.Fatalf("TLS defaults: %+v %v", c, err)
	}
	t.Setenv("TLS_CLIENT_PROFILE", "firefox_148")
	t.Setenv("TLS_PROXY_URL", "socks5://localhost:1080")
	c, err = Load(false)
	if err != nil || c.TLSClientProfile != "firefox_148" || c.TLSProxyURL != "socks5://localhost:1080" {
		t.Fatalf("TLS configuration: %+v %v", c, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"TLS_CLIENT_PROFILE", "unknown"},
		{"TLS_PROXY_URL", "http://secret:pass@example.com"},
		{"TLS_PROXY_URL", "http://example.com/path"},
		{"TLS_PROXY_URL", "ftp://example.com"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			envDefaults(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load(false)
			if err == nil || strings.Contains(err.Error(), "secret:pass") {
				t.Fatalf("invalid or leaking TLS configuration: %v", err)
			}
		})
	}
}
func TestConfigDefaultsAndChannelAdmins(t *testing.T) {
	envDefaults(t)
	c, err := Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != time.Minute || c.HTTPTimeout != 15*time.Second || c.StateFile != "/data/state.json" || c.Location.String() != "Asia/Shanghai" || len(c.URLs) != 3 || c.NotificationChatID != 42 || !c.AdminIDs[42] || c.AffiliateID != "1194" {
		t.Fatalf("defaults %+v", c)
	}
	if c.FetchMode != "browser" || c.BrowserTimeout != 45*time.Second || c.BrowserNoSandbox || c.BrowserProfileDir != "" {
		t.Fatalf("browser defaults: %+v", c)
	}
	t.Setenv("TELEGRAM_CHANNEL_ID", "-10012345")
	t.Setenv("TELEGRAM_ADMIN_IDS", "42, 43")
	c, err = Load(true)
	if err != nil || c.NotificationChatID != -10012345 || !c.AdminIDs[43] {
		t.Fatalf("channel/admins %+v %v", c, err)
	}
}
func TestInvalidConfig(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"TELEGRAM_BOT_TOKEN", ""}, {"TELEGRAM_CHAT_ID", "0"}, {"TELEGRAM_ADMIN_IDS", "42,-1"}, {"TELEGRAM_CHANNEL_ID", "public-channel"}, {"CHECK_INTERVAL_SECONDS", "0"}, {"HTTP_TIMEOUT_SECONDS", "2"}, {"LOG_LEVEL", "banana"}, {"TZ", "Invalid/Zone"}, {"SPARTAN_AFFILIATE_ID", "oops"}, {"SPARTAN_URLS", "http://example.com"}, {"SPARTAN_URLS", "https://secret:pass@example.com"}, {"SPARTAN_URLS", "https://example.com,https://example.com"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			envDefaults(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(true); err == nil {
				t.Fatalf("accepted invalid %s", tc.key)
			} else if strings.Contains(err.Error(), "secret:pass") {
				t.Fatal("leaked credentials")
			}
		})
	}
}
func TestVerifyNeedsNoSecrets(t *testing.T) {
	envDefaults(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	if _, err := Load(false); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserConfiguration(t *testing.T) {
	envDefaults(t)
	t.Setenv("FETCH_MODE", "http")
	t.Setenv("STATE_FILE", "local/state.json")
	t.Setenv("BROWSER_TIMEOUT_SECONDS", "60")
	t.Setenv("BROWSER_NO_SANDBOX", "true")
	t.Setenv("BROWSER_PROXY_URL", "socks5://localhost:1080")
	c, err := Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if c.FetchMode != "http" || c.BrowserProfileDir != "" || c.BrowserTimeout != time.Minute || !c.BrowserNoSandbox {
		t.Fatalf("browser configuration %+v", c)
	}
	t.Setenv("BROWSER_PROFILE_DIR", "custom-profile")
	c, err = Load(true)
	if err != nil || c.BrowserProfileDir != "custom-profile" {
		t.Fatalf("explicit profile configuration: %+v %v", c, err)
	}
	for _, tc := range []struct{ key, value string }{{"FETCH_MODE", "automatic"}, {"BROWSER_TIMEOUT_SECONDS", "0"}, {"BROWSER_NO_SANDBOX", "maybe"}, {"BROWSER_PROXY_URL", "http://secret:pass@example.com"}, {"BROWSER_PROXY_URL", "http://example.com/path"}, {"BROWSER_PROXY_URL", "ftp://example.com"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			envDefaults(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(true); err == nil {
				t.Fatal("invalid browser config accepted")
			} else if strings.Contains(err.Error(), "secret:pass") {
				t.Fatal("proxy credentials leaked")
			}
		})
	}
}
