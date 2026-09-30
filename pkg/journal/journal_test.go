package journal

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/trading"
)

func open(t *testing.T) *Journal {
	t.Helper()
	t.Setenv("OKU_STATE_DIR", t.TempDir())
	j, err := Open("p")
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestSameIDDifferentPayloadRejected(t *testing.T) {
	j := open(t)
	in := trading.Intent{Version: 1, ID: "abc", Kind: "order", Size: "1"}
	if _, created, err := j.Create(in); err != nil || !created {
		t.Fatal(err)
	}
	if _, created, err := j.Create(in); err != nil || created {
		t.Fatalf("idempotent re-prepare failed: created=%v err=%v", created, err)
	}
	in.Size = "2"
	if _, _, err := j.Create(in); !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
}

func TestUnknownPersistsAcrossReopen(t *testing.T) {
	j := open(t)
	r, _, _ := j.Create(trading.Intent{Version: 1, ID: "abc", Kind: "order"})
	r.State = StateUnknown
	if err := j.Put(r, "timeout"); err != nil {
		t.Fatal(err)
	}
	j2, _ := Open("p")
	un, err := j2.Unresolved()
	if err != nil || len(un) != 1 || un[0].State != StateUnknown {
		t.Fatalf("unresolved=%v err=%v", un, err)
	}
}

func TestNonceMonotonicUnderClockRollback(t *testing.T) {
	j := open(t)
	now := time.UnixMilli(2_000_000)
	j.now = func() time.Time { return now }
	a, _ := j.NextNonce("0xabc")
	now = time.UnixMilli(1_000_000) // clock went backwards
	b, _ := j.NextNonce("0xabc")
	j2, _ := Open("p") // restart
	j2.now = j.now
	c, _ := j2.NextNonce("0xabc")
	if !(a < b && b < c) {
		t.Fatalf("nonces not strictly increasing: %d %d %d", a, b, c)
	}
}

func TestCorruptNonceStateRefuses(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":   "not json",
		"truncated": "17",
		"negative":  "-5",
		"zero":      "0",
		"huge":      "9223372036854775807",
		"empty":     "",
	} {
		j := open(t)
		p := filepath.Join(j.dir, ".nonce-0xabc")
		_ = os.WriteFile(p, []byte(content), 0o600)
		n, err := j.NextNonce("0xabc")
		if name == "truncated" {
			// "17" is a syntactically valid positive nonce; it must still
			// only move forward.
			if err != nil || n <= 17 {
				t.Errorf("%s: n=%d err=%v", name, n, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted, got nonce %d", name, n)
		}
	}
}

// Concurrent processes serialize through Lock and never share a nonce.
func TestNonceUniqueUnderConcurrency(t *testing.T) {
	j := open(t)
	fixed := time.UnixMilli(5_000)
	var mu sync.Mutex
	seen := map[int64]bool{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jj, _ := Open("p")
			jj.now = func() time.Time { return fixed }
			unlock, err := jj.Lock()
			if err != nil {
				t.Error(err)
				return
			}
			n, err := jj.NextNonce("0xabc")
			unlock()
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			if seen[n] {
				t.Errorf("nonce %d reused", n)
			}
			seen[n] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	_ = j
}
