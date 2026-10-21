package spartan

import (
	"os"
	"strings"
	"testing"
)

const source = "https://billing.spartanhost.net/store/cmin2-premium-kvm-vps-seattle"

func TestParseFixture(t *testing.T) {
	f, err := os.Open("../../testdata/products.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	products, issues, err := Parse(f, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 3 || len(issues) != 1 {
		t.Fatalf("products=%d issues=%v", len(products), issues)
	}
	for i, want := range []int{0, 1, 12} {
		p := products[i]
		if p.AvailableQuantity != want {
			t.Errorf("stock %d = %d", i, p.AvailableQuantity)
		}
		if p.Category != "AS9929-CMIN2 Premium KVM VPS - Seattle" || p.BillingPeriod != "Monthly" || p.SourceURL != source || p.Provider != "Spartan Host" {
			t.Errorf("required fields: %+v", p)
		}
	}
	p := products[0]
	if p.PlanName != "4096MB SEAKVM AS9929-CMIN2" || p.Price != "$24.00 USD" || p.OrderURL != source+"/4096mb-seakvm-cmin2" {
		t.Errorf("fields %+v", p)
	}
	if p.CPU == "" || p.Storage == "" || p.Bandwidth == "" || p.RAM != "4096MB DDR5 RAM" {
		t.Errorf("hardware %+v", p)
	}
	if products[1].PlanName != "6144MB SEAKVM AS9929-CMIN2" || products[1].Price != "$36.00 USD" {
		t.Errorf("nested product %+v", products[1])
	}
	if products[2].Key != "billing.spartanhost.net:pid:103" || products[1].CPU != "" {
		t.Errorf("key/optional fields %+v", products[2])
	}
}

func card(qty string) string {
	return `<h1>Category</h1><div class="product"><header>4096MB Plan <span class="qty">` + qty + `</span></header><div class="product-pricing">$24.00 USD Monthly</div><a href="/cart.php?a=add&amp;pid=10">Order Now</a></div>`
}

func TestInvalidHTMLIsUnknownNotZero(t *testing.T) {
	for _, input := range []string{"", `<html>Just a moment...</html>`, card("unknown"), card("-1 Available"), card("0 Available 1 Available"), strings.ReplaceAll(card("0 Available"), "$24.00 USD", ""), strings.ReplaceAll(card("0 Available"), "Monthly", ""), strings.ReplaceAll(card("0 Available"), "/cart.php?a=add&amp;pid=10", "https://evil.example/cart.php?pid=10")} {
		p, _, err := Parse(strings.NewReader(input), source)
		if err == nil || len(p) != 0 {
			t.Errorf("must reject unknown HTML: %q => %+v %v", input, p, err)
		}
	}
}

func TestWhitespaceFallbackAndStableIdentity(t *testing.T) {
	original := card("12 Available")
	a, _, err := Parse(strings.NewReader(original), source)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(original, `class="product"`, `class="changed-template"`)
	changed = strings.ReplaceAll(changed, "4096MB Plan", "4096MB    Plan\n")
	changed = strings.ReplaceAll(changed, "12 Available", "12\n\tAvailable")
	b, _, err := Parse(strings.NewReader(changed), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || a[0].Key != b[0].Key || a[0].PlanName != b[0].PlanName {
		t.Fatalf("identity/whitespace: %+v %+v", a, b)
	}
	changed = strings.ReplaceAll(original, "4096MB Plan", "Renamed Plan")
	b, _, err = Parse(strings.NewReader(changed), source)
	if err != nil || a[0].Key != b[0].Key {
		t.Fatalf("name change must retain PID identity: %v", err)
	}
}
