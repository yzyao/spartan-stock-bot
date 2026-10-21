package telegram

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/yzyao/spartan-stock-bot/internal/model"
)

func TestFormatTimezoneSoldOutAndAffiliate(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	f := Formatter{Location: loc, AffiliateID: "1194"}
	started := time.Date(2026, 10, 21, 7, 30, 0, 0, time.UTC)
	p := model.Product{PlanName: "4096MB <Plan>", Category: "CMIN2", Price: "$24.00 USD", BillingPeriod: "Monthly", AvailableQuantity: 3, OrderURL: "https://billing.spartanhost.net/store/category/plan?currency=1&aff=2067"}
	n := f.Restock(p, started, started.Add(time.Minute))
	if !strings.Contains(n.Text, "2026-10-21 15:30:00") || !strings.Contains(n.Text, "4096MB <Plan>") || !strings.Contains(n.Text, "库存\n3") {
		t.Fatalf("text %s", n.Text)
	}
	u, err := url.Parse(n.Markup.InlineKeyboard[0][0].URL)
	if err != nil || u.Query().Get("aff") != "1194" || u.Query().Get("currency") != "1" || u.Path != "/store/category/plan" {
		t.Fatalf("affiliate order URL %v %v", u, err)
	}
	p.AvailableQuantity = 0
	n = f.Restock(p, started, started.Add(time.Minute))
	if !strings.Contains(n.Text, "❌ Spartan Host 已售罄") || !strings.Contains(n.Text, "售罄时间") || n.Markup.InlineKeyboard[0][0].Text != "🛒 查看商品" {
		t.Fatalf("sold out %v", n)
	}
}

func TestStatusSplitsWithinTelegramUTF16Limit(t *testing.T) {
	st := model.NewState()
	now := time.Now().UTC()
	for i := range 100 {
		key := fmt.Sprint(i)
		st.Products[key] = &model.ProductState{Product: model.Product{Key: key, Category: "Category " + fmt.Sprint(i/20), PlanName: strings.Repeat("🔥套餐", 50), Price: "$24 USD", BillingPeriod: "Monthly"}, LastCheckedAt: now.Add(-time.Hour)}
	}
	f := Formatter{Location: time.UTC}
	messages := f.Status(st, now)
	if len(messages) < 2 {
		t.Fatal("status not split")
	}
	for _, msg := range messages {
		if units(msg) > 4096 {
			t.Fatalf("message too long: %d", units(msg))
		}
	}
	if !strings.Contains(strings.Join(messages, ""), "数据可能过期") {
		t.Fatal("stale data warning missing")
	}
}
