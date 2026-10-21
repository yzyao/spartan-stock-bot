package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yzyao/spartan-stock-bot/internal/model"
	"github.com/yzyao/spartan-stock-bot/internal/state"
	"github.com/yzyao/spartan-stock-bot/internal/telegram"
)

type fakeProvider struct {
	products []model.Product
	err      error
	entered  chan struct{}
	release  chan struct{}
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) CheckInventory(ctx context.Context) (model.Inventory, error) {
	if p.entered != nil {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return model.Inventory{}, ctx.Err()
		}
	}
	return model.Inventory{Products: p.products, Categories: 3}, p.err
}

type action struct {
	kind string
	id   int64
	n    telegram.Notification
}
type fakeBot struct {
	actions          []action
	next             int64
	sendErr, editErr error
}

func (b *fakeBot) Send(_ context.Context, _ int64, n telegram.Notification) (int64, error) {
	if b.sendErr != nil {
		return 0, b.sendErr
	}
	b.next++
	b.actions = append(b.actions, action{kind: "send", id: b.next, n: n})
	return b.next, nil
}
func (b *fakeBot) Edit(_ context.Context, _ int64, id int64, n telegram.Notification) error {
	if b.editErr != nil {
		return b.editErr
	}
	b.actions = append(b.actions, action{kind: "edit", id: id, n: n})
	return nil
}

type fakeStore struct {
	saved          model.State
	fail           bool
	failActiveOnce bool
}

func (s *fakeStore) Save(st model.State) error {
	if s.fail {
		return errors.New("disk full")
	}
	if s.failActiveOnce {
		for _, p := range st.Products {
			if p.ActiveRestock != nil {
				s.failActiveOnce = false
				s.fail = true
				return errors.New("disk full after send")
			}
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.saved)
}
func product(stock int) model.Product {
	return model.Product{Key: "plan", Provider: "Spartan Host", Category: "CMIN2", PlanName: "4096MB SEAKVM", AvailableQuantity: stock, Price: "$24.00 USD", BillingPeriod: "Monthly", OrderURL: "https://billing.spartanhost.net/cart.php?a=add&pid=1", SourceURL: "https://billing.spartanhost.net/store/cmin2"}
}
func setup(t *testing.T) (*Monitor, *fakeProvider, *fakeBot, *fakeStore) {
	t.Helper()
	p := &fakeProvider{}
	b := &fakeBot{}
	s := &fakeStore{}
	m := New(p, b, s, model.NewState(), 42, telegram.Formatter{Location: time.UTC, AffiliateID: "1194"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m, p, b, s
}
func check(t *testing.T, m *Monitor) {
	t.Helper()
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRestockLifecycle(t *testing.T) {
	m, p, b, _ := setup(t)
	for _, stock := range []int{0, 0, 3, 3, 2, 8, 0, 0, 1, 0} {
		p.products = []model.Product{product(stock)}
		check(t, m)
	}
	var got []string
	var ids []int64
	for _, a := range b.actions {
		got = append(got, a.kind)
		ids = append(ids, a.id)
	}
	if !reflect.DeepEqual(got, []string{"send", "edit", "edit", "edit", "send", "edit"}) || !reflect.DeepEqual(ids, []int64{1, 1, 1, 1, 2, 2}) {
		t.Fatalf("actions=%v ids=%v", got, ids)
	}
	for _, i := range []int{3, 5} {
		if !strings.Contains(b.actions[i].n.Text, "❌ Spartan Host 已售罄") {
			t.Fatalf("not sold out: %+v", b.actions[i])
		}
	}
	s := m.Snapshot().Products["plan"]
	if s.ActiveRestock != nil || s.LastAvailable != 0 {
		t.Fatalf("event left active: %+v", s)
	}
}

func TestStartupAndRestartDoNotResend(t *testing.T) {
	m, p, b, _ := setup(t)
	p.products = []model.Product{product(3)}
	check(t, m)
	st := m.Snapshot()
	m = New(p, b, &fakeStore{}, st, 42, m.format, m.log)
	check(t, m)
	if len(b.actions) != 1 || st.Products["plan"].ActiveRestock.MessageID != 1 {
		t.Fatalf("restart resent: %+v", b.actions)
	}
	p.products = []model.Product{product(2)}
	check(t, m)
	if b.actions[1].id != 1 {
		t.Fatal("restart lost active message")
	}
}

func TestRestartFromJSONKeepsMessage(t *testing.T) {
	m, p, b, _ := setup(t)
	disk := &state.Store{Path: t.TempDir() + "/state.json", Log: m.log}
	m.store = disk
	p.products = []model.Product{product(3)}
	check(t, m)
	loaded, err := disk.Load()
	if err != nil {
		t.Fatal(err)
	}
	m = New(p, b, disk, loaded, 42, m.format, m.log)
	p.products = []model.Product{product(0)}
	check(t, m)
	p.products = []model.Product{product(2)}
	check(t, m)
	if len(b.actions) != 3 || b.actions[0].kind != "send" || b.actions[1].id != 1 || b.actions[2].kind != "send" || b.actions[2].id != 2 {
		t.Fatalf("JSON restart lifecycle %+v", b.actions)
	}
}

func TestUnknownNeverSoldOut(t *testing.T) {
	for _, cause := range []string{"HTTP timeout", "parser failure", "missing product"} {
		t.Run(cause, func(t *testing.T) {
			m, p, b, _ := setup(t)
			p.products = []model.Product{product(3)}
			check(t, m)
			p.products = nil
			if cause != "missing product" {
				p.err = errors.New(cause)
			}
			_, _ = m.Check(context.Background())
			s := m.Snapshot().Products["plan"]
			if len(b.actions) != 1 || s.Product.AvailableQuantity != 3 || s.ActiveRestock.MessageID != 1 {
				t.Fatalf("unknown converted to sold out: %+v %+v", s, b.actions)
			}
		})
	}
}

func TestTemporarySendAndEditFailuresRetry(t *testing.T) {
	m, p, b, _ := setup(t)
	p.products = []model.Product{product(3)}
	b.sendErr = &telegram.APIError{Code: 429, Description: "retry"}
	if _, err := m.Check(context.Background()); err == nil {
		t.Fatal("expected send failure")
	}
	if m.Snapshot().Products["plan"].ActiveRestock != nil {
		t.Fatal("marked unsent as sent")
	}
	b.sendErr = nil
	check(t, m)
	p.products = []model.Product{product(2)}
	b.editErr = errors.New("temporary timeout")
	if _, err := m.Check(context.Background()); err == nil {
		t.Fatal("expected edit failure")
	}
	s := m.Snapshot().Products["plan"]
	if s.LastAvailable != 3 || s.ActiveRestock.MessageID != 1 {
		t.Fatalf("lost retry: %+v", s)
	}
	b.editErr = nil
	check(t, m)
	if len(b.actions) != 2 || b.actions[1].kind != "edit" || b.actions[1].id != 1 {
		t.Fatalf("temporary failure caused replacement: %+v", b.actions)
	}
}

func TestFailedSoldOutMustCloseBeforeNewRestock(t *testing.T) {
	m, p, b, store := setup(t)
	now := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	p.products = []model.Product{product(3)}
	check(t, m)
	p.products = []model.Product{product(0)}
	b.editErr = errors.New("timeout")
	now = now.Add(time.Minute)
	if _, err := m.Check(context.Background()); err == nil {
		t.Fatal("expected sold out edit failure")
	}
	if store.saved.Products["plan"].LastAvailable != 3 || store.saved.Products["plan"].ActiveRestock.SoldOut == nil {
		t.Fatal("lost pending close")
	}
	// Restart from the last durable snapshot, then observe a new restock.
	m = New(p, b, store, store.saved, 42, m.format, m.log)
	m.now = func() time.Time { return now }
	p.products = []model.Product{product(2)}
	b.editErr = nil
	now = now.Add(time.Minute)
	check(t, m)
	if len(b.actions) != 3 || b.actions[1].kind != "edit" || b.actions[1].id != 1 || !strings.Contains(b.actions[1].n.Text, "已售罄") || b.actions[2].kind != "send" || b.actions[2].id != 2 {
		t.Fatalf("old message revived: %+v", b.actions)
	}
}

func TestPermanentEditFailureAllowsReplacement(t *testing.T) {
	m, p, b, _ := setup(t)
	p.products = []model.Product{product(3)}
	check(t, m)
	p.products = []model.Product{product(2)}
	b.editErr = &telegram.APIError{Code: 400, Description: "Bad Request: message to edit not found"}
	check(t, m)
	if len(b.actions) != 2 || b.actions[1].kind != "send" || m.Snapshot().Products["plan"].ActiveRestock.MessageID != 2 {
		t.Fatalf("replacement missing %+v", b.actions)
	}
	p.products = []model.Product{product(0)}
	check(t, m)
	if m.Snapshot().Products["plan"].ActiveRestock != nil || len(b.actions) != 2 {
		t.Fatal("missing sold-out message must close without new message")
	}
}

func TestMetadataEditsPreserveRestockTime(t *testing.T) {
	m, p, b, _ := setup(t)
	now := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	p.products = []model.Product{product(3)}
	check(t, m)
	started := m.Snapshot().Products["plan"].ActiveRestock.RestockStartedAt
	for _, change := range []func(*model.Product){func(p *model.Product) { p.Price = "$25 USD" }, func(p *model.Product) { p.BillingPeriod = "Quarterly" }, func(p *model.Product) { p.PlanName = "New Name" }, func(p *model.Product) { p.OrderURL += "&currency=1" }} {
		change(&p.products[0])
		now = now.Add(time.Minute)
		check(t, m)
	}
	if len(b.actions) != 5 {
		t.Fatalf("metadata actions %d", len(b.actions))
	}
	for _, a := range b.actions[1:] {
		if a.kind != "edit" || a.id != 1 {
			t.Fatal("metadata started new event")
		}
	}
	if !m.Snapshot().Products["plan"].ActiveRestock.RestockStartedAt.Equal(started) {
		t.Fatal("discovery time changed")
	}
}

func TestNoOverlappingChecks(t *testing.T) {
	m, p, _, _ := setup(t)
	p.entered = make(chan struct{})
	p.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := m.Check(context.Background()); done <- err }()
	<-p.entered
	if _, err := m.Check(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("second check started: %v", err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStateFailureBlocksNotificationAndRetainsSentID(t *testing.T) {
	m, p, b, s := setup(t)
	p.products = []model.Product{product(3)}
	s.fail = true
	if _, err := m.Check(context.Background()); err == nil || len(b.actions) != 0 {
		t.Fatal("sent before durable intent")
	}
	s.fail = false
	s.failActiveOnce = true
	if _, err := m.Check(context.Background()); err == nil || len(b.actions) != 1 {
		t.Fatalf("expected disk failure after accepted send: %+v %v", b.actions, err)
	}
	if _, err := m.Check(context.Background()); err == nil || len(b.actions) != 1 {
		t.Fatal("unflushed ID allowed duplicate")
	}
	s.fail = false
	check(t, m)
	if len(b.actions) != 1 || s.saved.Products["plan"].ActiveRestock.MessageID != 1 {
		t.Fatal("successful send lost after disk recovery")
	}
}

func TestAmbiguousSendCooldownSurvivesRestart(t *testing.T) {
	m, p, b, s := setup(t)
	now := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	p.products = []model.Product{product(3)}
	b.sendErr = &telegram.APIError{Description: "response unknown", Ambiguous: true}
	if _, err := m.Check(context.Background()); err == nil {
		t.Fatal("expected ambiguity")
	}
	m = New(p, b, s, s.saved, 42, m.format, m.log)
	m.now = func() time.Time { return now }
	b.sendErr = nil
	if _, err := m.Check(context.Background()); err == nil || len(b.actions) != 0 {
		t.Fatal("immediate ambiguous resend")
	}
	now = now.Add(5 * time.Minute)
	check(t, m)
	if len(b.actions) != 1 {
		t.Fatal("never retried")
	}
}

func TestSnapshotIsIndependent(t *testing.T) {
	m, p, _, _ := setup(t)
	p.products = []model.Product{product(2)}
	check(t, m)
	snapshot := m.Snapshot()
	snapshot.Products["plan"].ActiveRestock.MessageID = 99
	delete(snapshot.Products, "plan")
	if m.Snapshot().Products["plan"].ActiveRestock.MessageID != 1 {
		t.Fatal("snapshot mutated monitor")
	}
}
