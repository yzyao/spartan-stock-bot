package spartan

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/yzyao/spartan-stock-bot/internal/model"
)

const UserAgent = "SpartanStockBot/1.0"
const maxPageBytes = 4 << 20

func HTTPClient(timeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 16
	t.MaxIdleConnsPerHost = 4
	t.IdleConnTimeout = 90 * time.Second
	t.ResponseHeaderTimeout = timeout
	return &http.Client{Transport: t, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != "https" || req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("unexpected redirect host or scheme")
		}
		return nil
	}}
}

type Client struct {
	HTTP    *http.Client
	URLs    []string
	Log     *slog.Logger
	Fetcher PageFetcher
}

type PageFetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}

func (c *Client) Name() string { return "Spartan Host" }
func (c *Client) CheckInventory(ctx context.Context) (model.Inventory, error) {
	var result model.Inventory
	for _, source := range c.URLs {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		products, issues, err := c.fetch(ctx, source)
		result.Issues = append(result.Issues, issues...)
		if err != nil {
			result.Issues = append(result.Issues, model.Issue{SourceURL: source, Error: err.Error()})
			c.Log.Error("inventory_fetch_or_parser_error", "url", source, "error", err)
			continue
		}
		result.Categories++
		result.Products = append(result.Products, products...)
	}
	for _, issue := range result.Issues {
		c.Log.Warn("parser_warning", "url", issue.SourceURL, "plan", issue.Product, "error", issue.Error)
	}
	if result.Categories == 0 {
		return result, fmt.Errorf("all inventory pages failed; previous stock retained")
	}
	return result, nil
}

func (c *Client) fetch(ctx context.Context, source string) ([]model.Product, []model.Issue, error) {
	if c.Fetcher != nil {
		b, err := c.Fetcher.Fetch(ctx, source)
		if err != nil {
			return nil, nil, err
		}
		if len(b) == 0 || len(b) > maxPageBytes {
			return nil, nil, fmt.Errorf("empty or oversized browser HTML; inventory unknown")
		}
		return Parse(bytes.NewReader(b), source)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d; inventory unknown", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read HTML: %w", err)
	}
	if len(b) == 0 || len(b) > maxPageBytes {
		return nil, nil, fmt.Errorf("empty or oversized HTML; inventory unknown")
	}
	return Parse(bytes.NewReader(b), source)
}
