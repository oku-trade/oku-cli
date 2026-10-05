package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

func open(t *testing.T) *Journal {
	t.Helper()
	t.Setenv("OKU_STATE_DIR", t.TempDir())
	j, e := Open("p")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}
func intent(id, origin string) trading.Intent {
	return trading.Intent{Version: 1, ID: id, Kind: "order", Network: "mainnet", Account: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", APIURL: origin, CreatedAt: "2026-01-01T00:00:00Z"}
}
func created(t *testing.T, j *Journal, in trading.Intent) *Record {
	t.Helper()
	r, _, e := j.Create(in)
	if e != nil {
		t.Fatal(e)
	}
	r.State = StateCreated
	r.ActionID = "action-" + in.ID
	r.ActionJSON = "{}"
	if e = j.Put(r, ""); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestSameIDDifferentPayloadRejected(t *testing.T) {
	j := open(t)
	in := intent("abc", "https://one.example")
	if _, ok, e := j.Create(in); e != nil || !ok {
		t.Fatal(e)
	}
	if _, ok, e := j.Create(in); e != nil || ok {
		t.Fatalf("idempotence: %v %v", ok, e)
	}
	in.Size = "2"
	if _, _, e := j.Create(in); !errors.Is(e, ErrConflict) {
		t.Fatalf("want conflict, got %v", e)
	}
}
func TestUnknownPersistsAcrossReopen(t *testing.T) {
	j := open(t)
	r, _, e := j.Create(intent("abc", "https://one.example"))
	if e != nil {
		t.Fatal(e)
	}
	r.State = StateUnknown
	if e = j.Put(r, "timeout"); e != nil {
		t.Fatal(e)
	}
	j2, e := Open("p")
	if e != nil {
		t.Fatal(e)
	}
	defer j2.Close()
	un, e := j2.Unresolved()
	if e != nil || len(un) != 1 || un[0].State != StateUnknown {
		t.Fatalf("unresolved %v: %v", un, e)
	}
}
func TestNonceMonotonicUnderClockRollback(t *testing.T) {
	j := open(t)
	now := time.UnixMilli(2_000_000)
	j.now = func() time.Time { return now }
	a, e := j.NextNonce("0xabc")
	if e != nil {
		t.Fatal(e)
	}
	now = time.UnixMilli(1_000_000)
	b, e := j.NextNonce("0xABC")
	if e != nil {
		t.Fatal(e)
	}
	j2, e := Open("another")
	if e != nil {
		t.Fatal(e)
	}
	defer j2.Close()
	j2.now = j.now
	c, e := j2.NextNonce("0xabc")
	if e != nil || !(a < b && b < c) {
		t.Fatalf("nonces %d %d %d: %v", a, b, c, e)
	}
}
func TestCorruptNonceStateRefuses(t *testing.T) {
	j := open(t)
	if _, e := j.db.Exec(`INSERT INTO nonces(signer,last_nonce) VALUES('0xabc',9223372036854775807)`); e != nil {
		t.Fatal(e)
	}
	if _, e := j.NextNonce("0xabc"); e == nil {
		t.Fatal("exhausted nonce accepted")
	}
}
func TestNonceUniqueUnderConcurrency(t *testing.T) {
	j := open(t)
	var mu sync.Mutex
	seen := map[int64]bool{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jj, e := Open("q")
			if e != nil {
				t.Error(e)
				return
			}
			defer jj.Close()
			jj.now = func() time.Time { return time.UnixMilli(5_000) }
			n, e := jj.NextNonce("0xabc")
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			if seen[n] {
				t.Errorf("nonce reused: %d", n)
			}
			seen[n] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	_ = j
}
func TestBeginSubmissionGatesAcrossProfilesAndOrigins(t *testing.T) {
	j := open(t)
	j2, e := OpenBinding(Binding{Profile: "other", Network: "mainnet", Account: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Origin: "https://two.example"})
	if e != nil {
		t.Fatal(e)
	}
	defer j2.Close()
	a := created(t, j, intent("one", "https://one.example"))
	b := created(t, j2, intent("two", "https://two.example"))
	n, e := j.BeginSubmission(a, "0xsigner")
	if e != nil || n <= 0 {
		t.Fatalf("first allocation %d %v", n, e)
	}
	if _, e = j2.BeginSubmission(b, "0xsigner"); !errors.Is(e, ErrUnresolved) {
		t.Fatalf("account conflict bypassed: %v", e)
	}
	again, e := j.Get("one")
	if e != nil || again.State != StateSubmitting || again.Nonce != n {
		t.Fatalf("durable pre-send %v %v", again, e)
	}
	if e = j.Put(&Record{Intent: a.Intent, Digest: a.Digest, State: StateAbandoned, Nonce: n}, ""); e == nil {
		t.Fatal("possibly sent action abandoned")
	}
	a.State = StateSubmitted
	a.ServerState = "filled"
	if e = j.Put(a, "reconciled"); e != nil {
		t.Fatal(e)
	}
	n2, e := j2.BeginSubmission(b, "0xsigner")
	if e != nil || n2 <= n {
		t.Fatalf("post reconciliation nonce %d <= %d: %v", n2, n, e)
	}
}
func TestLegacyImportAndBarrier(t *testing.T) {
	base := t.TempDir()
	t.Setenv("OKU_STATE_DIR", base)
	dir := filepath.Join(base, "journal", "p")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	in := intent("legacy", "https://one.example")
	r := Record{Intent: in, Digest: in.Digest(), State: StateSubmitting, Nonce: 200, ActionID: "action", UpdatedAt: "2026-01-01T00:00:00Z", History: []Transition{{At: "2026-01-01T00:00:00Z", State: StateSubmitting}}}
	raw, _ := json.Marshal(r)
	if e := os.WriteFile(filepath.Join(dir, "legacy.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, ".nonce-0xSIGNER"), []byte("100"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open("p"); !errors.Is(e, ErrMigrationRequired) {
		t.Fatalf("automatic migration or missing barrier: %v", e)
	}
	if e := MigrateLegacy(); e != nil {
		t.Fatal(e)
	}
	if e := MigrateLegacy(); e != nil {
		t.Fatalf("completed migration rerun: %v", e)
	}
	j, e := Open("p")
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if _, _, e = j.Create(intent("post-migration", "https://one.example")); e != nil {
		t.Fatal(e)
	}
	if e = MigrateLegacy(); e != nil {
		t.Fatalf("migration rerun with new records: %v", e)
	}
	if reopened, e := Open("p"); e != nil {
		t.Fatalf("rerun blocked Open: %v", e)
	} else {
		_ = reopened.Close()
	}
	un, e := j.Unresolved()
	if e != nil || len(un) != 1 || un[0].Nonce != 200 {
		t.Fatalf("lost unresolved record %v %v", un, e)
	}
	j.now = func() time.Time { return time.UnixMilli(1) }
	n, e := j.NextNonce("0xsigner")
	if e != nil || n != 201 {
		t.Fatalf("lost legacy nonce %d %v", n, e)
	}
	if e = os.MkdirAll(filepath.Join(base, "journal", "new-profile"), 0700); e == nil {
		t.Fatal("old binary directory creation did not fail")
	}
	for _, name := range []string{"journal.sqlite", "journal", "journal-legacy-backup"} {
		st, e := os.Stat(filepath.Join(base, name))
		if e != nil {
			t.Fatal(e)
		}
		want := os.FileMode(0600)
		if st.IsDir() {
			want = 0700
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s permissions %o", name, st.Mode().Perm())
		}
	}
}

func TestMigrateLegacyFreshSQLiteNoop(t *testing.T) {
	j := open(t)
	defer j.Close()
	if err := MigrateLegacy(); err != nil {
		t.Fatalf("fresh migration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(j.base, "journal-migration-incomplete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete marker after no-op: %v", err)
	}
	again, err := OpenBinding(Binding{Profile: "other"})
	if err != nil {
		t.Fatalf("fresh migration bricked journal: %v", err)
	}
	defer again.Close()
}
func TestLegacyCorruptionAndActiveProcessFailClosed(t *testing.T) {
	j := open(t)
	base := j.base
	_ = j.Close()
	if e := os.Remove(filepath.Join(base, "journal")); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(base, "journal", "p")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := MigrateLegacy(); e == nil {
		t.Fatal("corrupt legacy record imported")
	}
	if _, e := os.Stat(filepath.Join(base, "journal-legacy-backup", "p", "bad.json")); e != nil {
		t.Fatal("legacy data lost", e)
	}
}

func TestLegacyActiveLockBlocksMigration(t *testing.T) {
	base := t.TempDir()
	t.Setenv("OKU_STATE_DIR", base)
	dir := filepath.Join(base, "journal", "p")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err = MigrateLegacy(); err == nil {
		t.Fatal("migration accepted active legacy process")
	}
	if _, err = os.Stat(filepath.Join(base, "journal-legacy-backup", "p")); err != nil {
		t.Fatal("legacy backup missing despite lock", err)
	}
	if _, err = Open("p"); !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("incomplete migration opened: %v", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err = MigrateLegacy(); err != nil {
		t.Fatalf("resume after active process: %v", err)
	}
	if _, err = Open("p"); err != nil {
		t.Fatalf("completed migration unavailable: %v", err)
	}
}

func TestSymlinkJournalDatabaseRejected(t *testing.T) {
	base := t.TempDir()
	t.Setenv("OKU_STATE_DIR", base)
	target := filepath.Join(base, "unrelated")
	if err := os.WriteFile(target, []byte("do not modify"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "journal.sqlite")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open("p"); err == nil {
		t.Fatal("symlink database accepted")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "do not modify" {
		t.Fatalf("external file changed: %q %v", raw, err)
	}
}
func TestBaselineBinaryDowngradeBarrier(t *testing.T) {
	binary := os.Getenv("OKU_BASELINE_BINARY")
	if binary == "" {
		t.Skip("set OKU_BASELINE_BINARY to baseline executable for actual downgrade check")
	}
	j := open(t)
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(j.base, "config"))
	f := &config.File{Version: 1, DefaultProfile: "p", Profiles: map[string]*config.Profile{"p": {Network: "mainnet", APIURL: "https://one.example", Account: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	if e := f.Save(); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"--profile", "p", "orders", "status", "--intent", "abc"}, {"--profile", "p", "orders", "submit", "--intent", "abc", "--digest", "deadbeef"}} {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "OKU_STATE_DIR="+j.base, "OKU_CONFIG_DIR="+filepath.Join(j.base, "config"))
		output, e := cmd.CombinedOutput()
		if e == nil || !strings.Contains(string(output), "opening journal") {
			t.Fatalf("old binary %v did not fail at journal path: %v %s", args, e, output)
		}
		t.Logf("old binary %v refused journal: %s", args, strings.TrimSpace(string(output)))
	}
}
func TestMultiprocessNonceWorker(t *testing.T) {
	if os.Getenv("JOURNAL_NONCE_CHILD") != "1" {
		return
	}
	j, e := Open(os.Getenv("JOURNAL_PROFILE"))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	j.now = func() time.Time { return time.UnixMilli(1000) }
	n, e := j.NextNonce("signer-global")
	if e != nil {
		t.Fatal(e)
	}
	fmt.Println("NONCE", n)
}
func TestMultiprocessNonceAndCrashState(t *testing.T) {
	j := open(t)
	r := created(t, j, intent("crash", "https://one.example"))
	if _, e := j.BeginSubmission(r, "signer-global"); e != nil {
		t.Fatal(e)
	}
	const workers = 6
	var wg sync.WaitGroup
	results := make(chan int64, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestMultiprocessNonceWorker$")
			cmd.Env = append(os.Environ(), "JOURNAL_NONCE_CHILD=1", "JOURNAL_PROFILE="+fmt.Sprint(i), "OKU_STATE_DIR="+j.base)
			out, e := cmd.CombinedOutput()
			if e != nil {
				t.Errorf("child: %v %s", e, out)
				return
			}
			var n int64
			if _, e = fmt.Sscanf(string(out), "NONCE %d", &n); e != nil {
				t.Errorf("child output %q: %v", out, e)
				return
			}
			results <- n
		}(i)
	}
	wg.Wait()
	close(results)
	seen := map[int64]bool{}
	for n := range results {
		if seen[n] {
			t.Fatalf("duplicate nonce %d", n)
		}
		seen[n] = true
	}
	if len(seen) != workers {
		t.Fatalf("got %d nonces", len(seen))
	}
	j2, e := Open("p")
	if e != nil {
		t.Fatal(e)
	}
	defer j2.Close()
	un, e := j2.Unresolved()
	if e != nil || len(un) != 1 || un[0].Nonce != r.Nonce {
		t.Fatalf("crash recovery %v %v", un, e)
	}
}
func TestRejectDefiniteOnlyFromSubmitting(t *testing.T) {
	j := open(t)
	a := created(t, j, intent("one", "https://one.example"))
	if e := j.RejectDefinite(a, "invalid_argument: bad"); e == nil {
		t.Fatal("definite rejection applied to an unsent record")
	}
	a = created(t, j, intent("two", "https://one.example"))
	n, e := j.BeginSubmission(a, "0xsigner")
	if e != nil {
		t.Fatal(e)
	}
	a.Nonce = n
	if e = j.RejectDefinite(a, ""); e == nil {
		t.Fatal("rejection without a reason accepted")
	}
	if e = j.RejectDefinite(a, "invalid_argument (http 400): bad signature"); e != nil {
		t.Fatal(e)
	}
	got, e := j.Get("two")
	if e != nil || got.State != StateRejected || got.LastError == "" || !strings.Contains(got.History[len(got.History)-1].Note, "bad signature") {
		t.Fatalf("not persisted: %+v %v", got, e)
	}
	if b, e := j.Blocking("mainnet", got.Intent.Account); e != nil || len(b) != 0 {
		t.Fatalf("rejected record still blocks: %v %v", b, e)
	}
}
