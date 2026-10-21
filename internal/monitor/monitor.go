package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yzyao/spartan-stock-bot/internal/model"
	"github.com/yzyao/spartan-stock-bot/internal/telegram"
)

var ErrBusy = errors.New("inventory check already running")

type Provider interface {
	Name() string
	CheckInventory(context.Context) (model.Inventory, error)
}
type Messenger interface {
	Send(context.Context, int64, telegram.Notification) (int64, error)
	Edit(context.Context, int64, int64, telegram.Notification) error
}
type Store interface{ Save(model.State) error }
type Summary struct {
	Categories, Products, Available, Failures int
	Duration                                  time.Duration
}

type Monitor struct {
	provider Provider
	bot      Messenger
	store    Store
	chatID   int64
	format   telegram.Formatter
	log      *slog.Logger
	gate     sync.Mutex
	mu       sync.RWMutex
	state    model.State
	dirty    bool
	now      func() time.Time
}

func New(provider Provider, bot Messenger, store Store, st model.State, chatID int64, format telegram.Formatter, log *slog.Logger) *Monitor {
	return &Monitor{provider: provider, bot: bot, store: store, state: st, chatID: chatID, format: format, log: log, now: time.Now}
}

func (m *Monitor) Snapshot() model.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := model.NewState()
	for key, original := range m.state.Products {
		p := *original
		if original.PendingSend != nil {
			pending := *original.PendingSend
			p.PendingSend = &pending
		}
		if original.ActiveRestock != nil {
			a := *original.ActiveRestock
			if a.SoldOut != nil {
				zero := *a.SoldOut
				a.SoldOut = &zero
			}
			p.ActiveRestock = &a
		}
		st.Products[key] = &p
	}
	return st
}

func (m *Monitor) save() error {
	m.dirty = true
	if err := m.store.Save(m.state); err != nil {
		m.log.Error("state_save_failed", "error", err)
		return err
	}
	m.dirty = false
	return nil
}

func (m *Monitor) Check(ctx context.Context) (Summary, error) {
	if !m.gate.TryLock() {
		return Summary{}, ErrBusy
	}
	defer m.gate.Unlock()
	started := m.now()
	result := Summary{}
	m.log.Info("inventory_check_started", "provider", m.provider.Name())
	m.mu.Lock()
	if m.dirty {
		if err := m.save(); err != nil {
			m.mu.Unlock()
			return result, err
		}
	}
	m.mu.Unlock()
	inventory, providerErr := m.provider.CheckInventory(ctx)
	result.Categories = inventory.Categories
	result.Products = len(inventory.Products)
	result.Failures = len(inventory.Issues)
	var errs []error
	if providerErr != nil {
		errs = append(errs, providerErr)
	}
	seen := make(map[string]bool)
	for _, p := range inventory.Products {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if p.Key == "" || p.AvailableQuantity < 0 || seen[p.Key] {
			result.Failures++
			errs = append(errs, fmt.Errorf("provider returned invalid or duplicate product"))
			continue
		}
		seen[p.Key] = true
		if p.AvailableQuantity > 0 {
			result.Available++
		}
		m.mu.Lock()
		err := m.observe(ctx, p, m.now().UTC())
		m.mu.Unlock()
		if err != nil {
			result.Failures++
			errs = append(errs, fmt.Errorf("%s: %w", p.Key, err))
			m.log.Error("product_update_failed", "product_key", p.Key, "plan", p.PlanName, "error", err)
		}
		// A disk failure blocks further outbound notifications until in-memory state is durable.
		m.mu.RLock()
		dirty := m.dirty
		m.mu.RUnlock()
		if dirty {
			break
		}
	}
	m.mu.RLock()
	for key, p := range m.state.Products {
		if !seen[key] {
			m.log.Warn("product_unknown", "product_key", key, "url", p.Product.SourceURL, "last_stock", p.Product.AvailableQuantity, "reason", "not observed; retaining last known state")
		}
	}
	m.mu.RUnlock()
	result.Duration = m.now().Sub(started)
	m.log.Info("inventory_check_completed", "categories", result.Categories, "products", result.Products, "available", result.Available, "failures", result.Failures, "duration", result.Duration)
	return result, errors.Join(errs...)
}

func (m *Monitor) observe(ctx context.Context, p model.Product, now time.Time) error {
	s, exists := m.state.Products[p.Key]
	if !exists {
		s = &model.ProductState{}
		m.state.Products[p.Key] = s
	}
	old := s.Product
	if !exists || old.AvailableQuantity != p.AvailableQuantity {
		m.log.Info("inventory_changed", "category", p.Category, "product_key", p.Key, "plan", p.PlanName, "old_stock", old.AvailableQuantity, "new_stock", p.AvailableQuantity)
	}
	if exists && metadataChanged(old, p) {
		m.log.Info("product_metadata_changed", "product_key", p.Key, "old_price", old.Price, "new_price", p.Price)
	}
	s.Product = p
	s.LastCheckedAt = now
	if a := s.ActiveRestock; a != nil && p.AvailableQuantity == 0 && a.SoldOut == nil {
		zero := p
		a.SoldOut = &zero
		a.SoldOutAt = now
	}
	if p.AvailableQuantity == 0 {
		s.PendingSend = nil
	}
	// Persist observations and the pending close before touching Telegram.
	if err := m.save(); err != nil {
		return err
	}
	if a := s.ActiveRestock; a != nil && a.SoldOut != nil {
		if err := m.closeEvent(ctx, s); err != nil {
			return err
		}
	}
	if p.AvailableQuantity == 0 {
		s.LastAvailable = 0
		return m.save()
	}
	if s.ActiveRestock == nil {
		return m.send(ctx, s, now)
	}
	a := s.ActiveRestock
	if s.PendingSend != nil {
		return m.send(ctx, s, a.RestockStartedAt)
	}
	if a.Delivered == p {
		return nil
	}
	if err := m.bot.Edit(ctx, a.ChatID, a.MessageID, m.format.Restock(p, a.RestockStartedAt, now)); err != nil {
		if telegram.PermanentEdit(err) {
			m.log.Warn("telegram_active_message_unavailable", "product_key", p.Key, "message_id", a.MessageID, "error", err)
			return m.send(ctx, s, a.RestockStartedAt)
		}
		return fmt.Errorf("edit active restock: %w", err)
	}
	a.Delivered = p
	s.LastAvailable = p.AvailableQuantity
	m.log.Info("telegram_message_edited", "product_key", p.Key, "message_id", a.MessageID, "new_stock", p.AvailableQuantity)
	return m.save()
}

func metadataChanged(a, b model.Product) bool {
	a.AvailableQuantity = b.AvailableQuantity
	return a != b
}

func (m *Monitor) closeEvent(ctx context.Context, s *model.ProductState) error {
	a := s.ActiveRestock
	err := m.bot.Edit(ctx, a.ChatID, a.MessageID, m.format.Restock(*a.SoldOut, a.RestockStartedAt, a.SoldOutAt))
	if err != nil && !telegram.PermanentEdit(err) {
		return fmt.Errorf("close restock: %w", err)
	}
	if err != nil {
		m.log.Warn("telegram_sold_out_message_unavailable", "product_key", s.Product.Key, "message_id", a.MessageID, "error", err)
	}
	m.log.Info("product_sold_out", "product_key", s.Product.Key, "message_id", a.MessageID)
	s.ActiveRestock = nil
	s.PendingSend = nil
	s.LastAvailable = 0
	return m.save()
}

func (m *Monitor) send(ctx context.Context, s *model.ProductState, started time.Time) error {
	now := m.now().UTC()
	if s.PendingSend != nil && now.Before(s.PendingSend.RetryAfter) {
		return fmt.Errorf("restock send deferred until %s after uncertain delivery", s.PendingSend.RetryAfter.Format(time.RFC3339))
	}
	if s.PendingSend == nil {
		s.PendingSend = &model.PendingSend{StartedAt: started}
	}
	// Telegram has no sendMessage idempotency key. Persist a cooldown before sending so an
	// interrupted/ambiguous request is not immediately repeated on restart.
	s.PendingSend.RetryAfter = now.Add(5 * time.Minute)
	if err := m.save(); err != nil {
		return err
	}
	id, err := m.bot.Send(ctx, m.chatID, m.format.Restock(s.Product, s.PendingSend.StartedAt, s.LastCheckedAt))
	if err != nil {
		if !telegram.AmbiguousSend(err) {
			s.PendingSend.RetryAfter = now
			if e := m.save(); e != nil {
				return errors.Join(err, e)
			}
		}
		return fmt.Errorf("send restock: %w", err)
	}
	s.ActiveRestock = &model.ActiveRestock{ChatID: m.chatID, MessageID: id, RestockStartedAt: s.PendingSend.StartedAt, Delivered: s.Product}
	s.LastAvailable = s.Product.AvailableQuantity
	s.PendingSend = nil
	m.log.Info("telegram_message_sent", "product_key", s.Product.Key, "message_id", id, "new_stock", s.Product.AvailableQuantity)
	return m.save()
}

// Run schedules relative to completion, preventing catch-up bursts after a slow check.
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	for ctx.Err() == nil {
		if _, err := m.Check(ctx); err != nil && !errors.Is(err, ErrBusy) && ctx.Err() == nil {
			m.log.Error("inventory_check_failed", "error", err)
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
