package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yzyao/spartan-stock-bot/internal/model"
)

type Store struct {
	Path string
	Log  *slog.Logger
}

func (s *Store) Load() (model.State, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return model.NewState(), nil
	}
	if err != nil {
		return model.State{}, fmt.Errorf("read state: %w", err)
	}
	var st model.State
	d := json.NewDecoder(bytes.NewReader(b))
	err = d.Decode(&st)
	if err == nil {
		var extra any
		if e := d.Decode(&extra); e != io.EOF {
			err = fmt.Errorf("trailing state data")
		}
	}
	if err == nil && st.Version > 1 {
		return model.State{}, fmt.Errorf("unsupported state version %d; refusing to discard newer state", st.Version)
	}
	if err == nil && st.Version != 1 {
		err = fmt.Errorf("state version missing or invalid")
	}
	if err == nil {
		err = validate(st)
	}
	if err == nil {
		return st, nil
	}
	backup := s.Path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if e := os.Rename(s.Path, backup); e != nil {
		return model.State{}, fmt.Errorf("state corrupt (%v), cannot preserve: %w", err, e)
	}
	s.Log.Error("state_corrupt_recovered", "error", err, "backup", backup, "warning", "initial available products may notify again")
	return model.NewState(), nil
}

func validate(st model.State) error {
	if st.Products == nil {
		return fmt.Errorf("state products missing")
	}
	for k, p := range st.Products {
		if p == nil || k == "" || p.Product.Key != k || p.Product.AvailableQuantity < 0 || p.LastAvailable < 0 {
			return fmt.Errorf("invalid product state %q", k)
		}
		if a := p.ActiveRestock; a != nil {
			if a.ChatID == 0 || a.MessageID <= 0 || a.RestockStartedAt.IsZero() || a.Delivered.Key != k {
				return fmt.Errorf("invalid active restock %q", k)
			}
		}
	}
	return nil
}

// Save uses a same-directory temporary file and fsync before replacing the destination.
func (s *Store) Save(st model.State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.Path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp := s.Path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open temporary state: %w", err)
	}
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err = os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	if runtime.GOOS != "windows" {
		df, e := os.Open(dir)
		if e != nil {
			return fmt.Errorf("open state directory for sync: %w", e)
		}
		e = df.Sync()
		ce := df.Close()
		if e != nil {
			return fmt.Errorf("sync state directory: %w", e)
		}
		if ce != nil {
			return ce
		}
	}
	s.Log.Debug("state_saved", "products", len(st.Products))
	return nil
}
