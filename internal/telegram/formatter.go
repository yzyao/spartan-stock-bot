package telegram

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/yzyao/spartan-stock-bot/internal/model"
)

type Button struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}
type Markup struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}
type Notification struct {
	Text   string
	Markup Markup
}
type Formatter struct {
	Location    *time.Location
	AffiliateID string
}

func (f Formatter) timestamp(t time.Time) string {
	return t.In(f.Location).Format("2006-01-02 15:04:05")
}
func (f Formatter) Restock(p model.Product, started, updated time.Time) Notification {
	title, status, startLabel, endLabel, button := "🔥 Spartan Host 补货", "✅ 有货", "补货发现", "最后更新", "🛒 立即购买"
	if p.AvailableQuantity == 0 {
		title = "❌ Spartan Host 已售罄"
		status = "❌ 已售罄"
		startLabel = "本轮补货"
		endLabel = "售罄时间"
		button = "🛒 查看商品"
	}
	text := fmt.Sprintf("%s\n\n📦 套餐\n%s\n\n🌐 线路\n%s\n\n💰 价格\n%s / %s\n\n📊 库存\n%d\n\n状态\n%s\n\n🕐 %s\n%s\n\n🕐 %s\n%s", title, truncate(p.PlanName, 500), truncate(p.Category, 500), truncate(p.Price, 100), truncate(p.BillingPeriod, 100), p.AvailableQuantity, status, startLabel, f.timestamp(started), endLabel, f.timestamp(updated))
	order := p.OrderURL
	if u, err := url.Parse(order); err == nil && f.AffiliateID != "" {
		q := u.Query()
		q.Set("aff", f.AffiliateID)
		u.RawQuery = q.Encode()
		order = u.String()
	}
	return Notification{Text: text, Markup: Markup{InlineKeyboard: [][]Button{{{Text: button, URL: order}}}}}
}

func (f Formatter) Status(st model.State, now time.Time) []string {
	var keys []string
	for k := range st.Products {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := st.Products[keys[i]].Product, st.Products[keys[j]].Product
		if a.Category == b.Category {
			return a.PlanName < b.PlanName
		}
		return a.Category < b.Category
	})
	blocks := []string{"📡 Spartan Host 库存状态\n库存为最后一次成功解析的结果；失败时保留旧值。"}
	category := ""
	for _, key := range keys {
		s := st.Products[key]
		p := s.Product
		if p.Category != category {
			category = p.Category
			blocks = append(blocks, "【"+truncate(category, 500)+"】")
		}
		icon := "❌"
		if p.AvailableQuantity > 0 {
			icon = "✅"
		}
		age := ""
		if now.Sub(s.LastCheckedAt) > 3*time.Minute {
			age = " ⚠️ 数据可能过期"
		}
		blocks = append(blocks, fmt.Sprintf("%s %s\n库存: %d\n价格: %s / %s\n最后检查: %s%s", icon, truncate(p.PlanName, 500), p.AvailableQuantity, truncate(p.Price, 100), truncate(p.BillingPeriod, 100), f.timestamp(s.LastCheckedAt), age))
	}
	if len(keys) == 0 {
		blocks = append(blocks, "暂无库存数据，等待首次成功检查。")
	}
	var messages []string
	current := ""
	for _, block := range blocks {
		if units(current)+units(block)+2 > 3800 {
			messages = append(messages, current)
			current = "📡 库存状态（续）"
		}
		if current != "" {
			current += "\n\n"
		}
		current += block
	}
	if current != "" {
		messages = append(messages, current)
	}
	return messages
}

func units(s string) int { return len(utf16.Encode([]rune(s))) }
func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

func Help(interval time.Duration) string {
	return fmt.Sprintf("🤖 SpartanStockBot\n\n/status\n查看当前所有库存状态\n\n/check\n立即执行一次库存检查\n\n/help 或 /start\n查看帮助信息\n\n自动检查间隔:\n%g 秒", interval.Seconds())
}
