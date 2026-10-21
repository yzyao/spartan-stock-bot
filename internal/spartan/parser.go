package spartan

import (
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/yzyao/spartan-stock-bot/internal/model"
	"golang.org/x/net/html"
)

var availableRE = regexp.MustCompile(`(?i)(?:^|\s)([0-9]+)\s+Available\b`)
var quantityRE = regexp.MustCompile(`(?i)\s*[0-9]+\s+Available\b`)
var priceRE = regexp.MustCompile(`(?:[$€£]\s*[0-9][0-9,.]*|[0-9][0-9,.]*\s+(?:USD|EUR|GBP|CAD|AUD))(?:\s*(?:USD|EUR|GBP|CAD|AUD))?`)
var periodRE = regexp.MustCompile(`(?i)\b(One\s+Time|Monthly|Quarterly|Semi[- ]?Annually|Annually|Biennially|Triennially|month|year)\b`)
var ramRE = regexp.MustCompile(`(?i)\b[0-9]+\s*(?:MB|GB)\b`)

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// nodeText adds boundaries at element edges, including <br> and nested quantity spans.
func nodeText(s *goquery.Selection) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			b.WriteByte(' ')
		}
	}
	for _, n := range s.Nodes {
		walk(n)
	}
	return clean(b.String())
}

func Parse(r io.Reader, source string) ([]model.Product, []model.Issue, error) {
	base, err := url.Parse(source)
	if err != nil || base.Host == "" {
		return nil, nil, fmt.Errorf("invalid source URL")
	}
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("parse HTML: %w", err)
	}
	category := nodeText(doc.Find("h1").First())
	if category == "" {
		return nil, nil, fmt.Errorf("category heading missing; unexpected HTML")
	}
	// WHMCS product cards; link ancestry fallback handles small template/class changes.
	cards := doc.Find(".product").FilterFunction(func(_ int, s *goquery.Selection) bool {
		return s.Find("a[href]").Length() > 0 && s.Find(".product").Length() == 0
	})
	if cards.Length() == 0 {
		doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
			if !strings.Contains(strings.ToLower(nodeText(a)), "order now") {
				return
			}
			for p := a.Parent(); p.Length() > 0 && !p.Is("body,html"); p = p.Parent() {
				if p.Find("header,h2,h3,h4,.product-name").Length() == 1 && availableRE.MatchString(nodeText(p)) {
					cards = cards.AddSelection(p)
					break
				}
			}
		})
	}
	if cards.Length() == 0 {
		return nil, nil, fmt.Errorf("no product cards found; inventory unknown")
	}
	var products []model.Product
	var issues []model.Issue
	seen := map[string]bool{}
	cards.Each(func(_ int, card *goquery.Selection) {
		p, e := parseCard(card, base, category)
		if e == nil && seen[p.Key] {
			e = fmt.Errorf("duplicate product key %s", p.Key)
		}
		if e != nil {
			issues = append(issues, model.Issue{SourceURL: source, Product: nodeText(card.Find(".product-name,h2,h3,h4,header").First()), Error: e.Error()})
			return
		}
		seen[p.Key] = true
		products = append(products, p)
	})
	if len(products) == 0 {
		return nil, issues, fmt.Errorf("no valid products; inventory unknown")
	}
	return products, issues, nil
}

func parseCard(card *goquery.Selection, base *url.URL, category string) (model.Product, error) {
	p := model.Product{Provider: "Spartan Host", Category: category, SourceURL: base.String()}
	heading := card.Find(".product-name,h2,h3,h4,header").First()
	p.PlanName = clean(quantityRE.ReplaceAllString(nodeText(heading), ""))
	qty := card.Find(".qty,.quantity,.stock").First()
	qtyText := nodeText(qty)
	if qty.Length() == 0 {
		qtyText = nodeText(heading)
	}
	matches := availableRE.FindAllStringSubmatch(qtyText, -1)
	if len(matches) != 1 {
		return p, fmt.Errorf("numeric Available value missing or ambiguous")
	}
	n, err := strconv.Atoi(matches[0][1])
	if err != nil {
		return p, fmt.Errorf("invalid Available quantity")
	}
	p.AvailableQuantity = n
	pricing := card.Find(".product-pricing,.pricing").First()
	if pricing.Length() == 0 {
		pricing = card
	}
	p.Price = clean(priceRE.FindString(nodeText(pricing)))
	p.BillingPeriod = clean(periodRE.FindString(nodeText(pricing)))
	var order *url.URL
	card.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		label := strings.ToLower(nodeText(a))
		id, _ := a.Attr("id")
		if !strings.Contains(label, "order now") && !strings.Contains(strings.ToLower(id), "order") {
			return true
		}
		href, _ := a.Attr("href")
		u, e := url.Parse(strings.TrimSpace(href))
		if e != nil || href == "" {
			return true
		}
		u = base.ResolveReference(u)
		if u.Scheme != "https" || !strings.EqualFold(u.Host, base.Host) || u.User != nil || u.Fragment != "" {
			return true
		}
		if u.Query().Get("pid") == "" && !strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/") {
			return true
		}
		order = u
		return false
	})
	if p.PlanName == "" || p.Price == "" || p.BillingPeriod == "" || order == nil {
		return p, fmt.Errorf("required name, price, billing period or safe order URL missing")
	}
	p.OrderURL = order.String()
	p.Key = productKey(base, order)
	p.RAM = clean(ramRE.FindString(p.PlanName))
	card.Find("li").Each(func(_ int, s *goquery.Selection) {
		v := nodeText(s)
		lower := strings.ToLower(v)
		switch {
		case strings.Contains(lower, "ram"):
			p.RAM = v
		case strings.Contains(lower, "cpu"):
			p.CPU = v
		case strings.Contains(lower, "storage"):
			p.Storage = v
		case strings.Contains(lower, "bandwidth") && p.Bandwidth == "":
			p.Bandwidth = v
		}
	})
	return p, nil
}

func productKey(base, order *url.URL) string {
	host := strings.ToLower(base.Host)
	if pid := order.Query().Get("pid"); pid != "" {
		return host + ":pid:" + strings.ToLower(clean(pid))
	}
	return host + ":" + strings.ToLower(path.Clean(order.Path))
}
