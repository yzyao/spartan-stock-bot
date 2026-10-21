package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

var DefaultURLs = []string{
	"https://billing.spartanhost.net/store/cmin2-premium-kvm-vps-seattle",
	"https://billing.spartanhost.net/store/ddos-protected-ssd-premium-kvm-vps-seattle",
	"https://billing.spartanhost.net/store/ddos-protected-ssd-e5-kvm-vps-seattle",
}

type Config struct {
	Token              string
	ChatID             int64
	NotificationChatID int64
	AdminIDs           map[int64]bool
	Interval           time.Duration
	HTTPTimeout        time.Duration
	StateFile          string
	Level              slog.Level
	Location           *time.Location
	URLs               []string
	AffiliateID        string
	FetchMode          string
	TLSClientProfile   string
	TLSProxyURL        string
	BrowserExecutable  string
	BrowserProfileDir  string
	BrowserProxyURL    string
	BrowserTimeout     time.Duration
	BrowserNoSandbox   bool
}

func Load(requireTelegram bool) (Config, error) {
	c := Config{Token: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")), StateFile: env("STATE_FILE", "/data/state.json"), URLs: append([]string(nil), DefaultURLs...), AffiliateID: env("SPARTAN_AFFILIATE_ID", "1194")}
	if id, err := strconv.ParseUint(c.AffiliateID, 10, 64); err != nil || id == 0 {
		return c, fmt.Errorf("SPARTAN_AFFILIATE_ID must be a positive integer")
	}
	var err error
	if requireTelegram {
		if c.Token == "" || !strings.Contains(c.Token, ":") || strings.ContainsAny(c.Token, "/\r\n \t?#") {
			return c, fmt.Errorf("TELEGRAM_BOT_TOKEN is missing or invalid")
		}
		c.ChatID, err = strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
		if err != nil || c.ChatID == 0 {
			return c, fmt.Errorf("TELEGRAM_CHAT_ID must be a nonzero integer")
		}
		c.NotificationChatID = c.ChatID
		if target := strings.TrimSpace(os.Getenv("TELEGRAM_CHANNEL_ID")); target != "" {
			c.NotificationChatID, err = strconv.ParseInt(target, 10, 64)
			if err != nil || c.NotificationChatID >= 0 {
				return c, fmt.Errorf("TELEGRAM_CHANNEL_ID must be a negative numeric channel ID (usually -100...)")
			}
		}
		c.AdminIDs = make(map[int64]bool)
		admins := env("TELEGRAM_ADMIN_IDS", strconv.FormatInt(c.ChatID, 10))
		for _, raw := range strings.Split(admins, ",") {
			id, e := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if e != nil || id <= 0 {
				return c, fmt.Errorf("TELEGRAM_ADMIN_IDS must contain positive Telegram user IDs")
			}
			c.AdminIDs[id] = true
		}
	}
	if c.Interval, err = seconds("CHECK_INTERVAL_SECONDS", 60, 1, 86400); err != nil {
		return c, err
	}
	if c.HTTPTimeout, err = seconds("HTTP_TIMEOUT_SECONDS", 15, 3, 300); err != nil {
		return c, err
	}
	c.FetchMode = env("FETCH_MODE", "browser")
	if c.FetchMode != "browser" && c.FetchMode != "http" && c.FetchMode != "tls" {
		return c, fmt.Errorf("FETCH_MODE must be browser, http or tls")
	}
	c.TLSClientProfile = env("TLS_CLIENT_PROFILE", "chrome_150")
	if c.TLSClientProfile != "chrome_150" && c.TLSClientProfile != "firefox_148" {
		return c, fmt.Errorf("TLS_CLIENT_PROFILE must be chrome_150 or firefox_148")
	}
	c.TLSProxyURL = strings.TrimSpace(os.Getenv("TLS_PROXY_URL"))
	if err = validateProxy("TLS_PROXY_URL", c.TLSProxyURL); err != nil {
		return c, err
	}
	c.BrowserExecutable = strings.TrimSpace(os.Getenv("BROWSER_EXECUTABLE"))
	c.BrowserProfileDir = strings.TrimSpace(os.Getenv("BROWSER_PROFILE_DIR"))
	c.BrowserProxyURL = strings.TrimSpace(os.Getenv("BROWSER_PROXY_URL"))
	if err = validateProxy("BROWSER_PROXY_URL", c.BrowserProxyURL); err != nil {
		return c, err
	}
	if c.BrowserTimeout, err = seconds("BROWSER_TIMEOUT_SECONDS", 45, 5, 300); err != nil {
		return c, err
	}
	c.BrowserNoSandbox, err = strconv.ParseBool(env("BROWSER_NO_SANDBOX", "false"))
	if err != nil {
		return c, fmt.Errorf("BROWSER_NO_SANDBOX must be true or false")
	}
	if err = c.Level.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("invalid LOG_LEVEL")
	}
	c.Location, err = time.LoadLocation(env("TZ", "Asia/Shanghai"))
	if err != nil {
		return c, fmt.Errorf("invalid TZ: %w", err)
	}
	if raw := os.Getenv("SPARTAN_URLS"); raw != "" {
		c.URLs = strings.Split(raw, ",")
	}
	seen := map[string]bool{}
	for i, raw := range c.URLs {
		u, e := url.Parse(strings.TrimSpace(raw))
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return c, fmt.Errorf("SPARTAN_URLS must contain absolute HTTPS URLs without credentials or fragments")
		}
		c.URLs[i] = u.String()
		if seen[c.URLs[i]] {
			return c, fmt.Errorf("duplicate SPARTAN_URLS entry")
		}
		seen[c.URLs[i]] = true
	}
	return c, nil
}

func validateProxy(key, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("%s must be an HTTP/HTTPS/SOCKS5 proxy URL without credentials, query or path", key)
	}
	return nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
func seconds(key string, def, min, max int) (time.Duration, error) {
	n, e := strconv.Atoi(env(key, strconv.Itoa(def)))
	if e != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", key, min, max)
	}
	return time.Duration(n) * time.Second, nil
}
