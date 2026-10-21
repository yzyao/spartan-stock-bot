package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yzyao/spartan-stock-bot/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "nested", "state.json"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}
func TestPersistenceAndAtomicReplacement(t *testing.T) {
	s := testStore(t)
	st, err := s.Load()
	if err != nil || len(st.Products) != 0 {
		t.Fatalf("missing state: %+v %v", st, err)
	}
	p := model.Product{Key: "plan", AvailableQuantity: 2}
	now := time.Now().UTC().Truncate(time.Second)
	st.Products[p.Key] = &model.ProductState{Product: p, LastAvailable: 2, LastCheckedAt: now, ActiveRestock: &model.ActiveRestock{ChatID: -100123, MessageID: 123, RestockStartedAt: now, Delivered: p}}
	if err = s.Save(st); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.Path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file remains: %v", err)
	}
	st.Products[p.Key].LastAvailable = 1
	if err = s.Save(st); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := reloaded.Products[p.Key]
	if r.LastAvailable != 1 || r.ActiveRestock.MessageID != 123 || r.ActiveRestock.ChatID != -100123 || !r.ActiveRestock.RestockStartedAt.Equal(now) {
		t.Fatalf("lost restock: %+v", r)
	}
	// A stale/interrupted temporary write must not affect the committed state.
	if err = os.WriteFile(s.Path+".tmp", []byte(`{"partial"`), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err = s.Load()
	if err != nil || reloaded.Products[p.Key].ActiveRestock.MessageID != 123 {
		t.Fatalf("temporary file was read: %v", err)
	}
}

func TestCorruptRecovery(t *testing.T) {
	for _, contents := range []string{`{broken`, `{}`, `null`, `{"version":0,"products":{}}`, `{"version":1,"products":null}`, `{"version":1,"products":{"bad":null}}`, `{"version":1,"products":{}} trailing`} {
		t.Run(contents, func(t *testing.T) {
			s := testStore(t)
			if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.Path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			st, err := s.Load()
			if err != nil || len(st.Products) != 0 {
				t.Fatalf("recovery: %+v %v", st, err)
			}
			backups, err := filepath.Glob(s.Path + ".corrupt-*")
			if err != nil || len(backups) != 1 {
				t.Fatalf("corrupt file not preserved: %v %v", backups, err)
			}
			b, err := os.ReadFile(backups[0])
			if err != nil || string(b) != contents {
				t.Fatalf("backup contents %q %v", b, err)
			}
			if err = s.Save(st); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnsupportedVersionPreserved(t *testing.T) {
	s := testStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte(`{"version":2,"products":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("future state must not be overwritten")
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatal("original state lost", err)
	}
}

func TestAtomicWriteFailurePreservesOldState(t *testing.T) {
	s := testStore(t)
	st := model.NewState()
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.Path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(st); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := s.Load(); err != nil {
		t.Fatalf("previous state damaged: %v", err)
	}
}
