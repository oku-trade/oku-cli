package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

// MigrateLegacy is an explicit, offline operation. It imports every profile and
// nonce before replacing the old binary's journal directory with a blocking file.
// It never reads credentials. The caller must ensure no old binaries are running;
// nonblocking legacy locks reject known active journal operations.
func MigrateLegacy() error {
	base, err := config.StateDir()
	if err != nil {
		return err
	}
	legacy := filepath.Join(base, "journal")
	backup := filepath.Join(base, "journal-legacy-backup")
	marker := filepath.Join(base, "journal-migration-incomplete")
	if err = os.MkdirAll(base, 0700); err != nil {
		return err
	}
	guard, err := os.OpenFile(filepath.Join(base, "journal.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer guard.Close()
	if err = syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("journal active: %w", err)
	}
	defer syscall.Flock(int(guard.Fd()), syscall.LOCK_UN)
	if _, e := os.Lstat(marker); errors.Is(e, os.ErrNotExist) {
		f, e := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
		if e = syncDir(base); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	if st, e := os.Lstat(backup); errors.Is(e, os.ErrNotExist) {
		st, e = os.Lstat(legacy)
		if e != nil {
			return e
		}
		if !st.IsDir() {
			return ErrMigrationRequired
		}
		if e = os.Rename(legacy, backup); e != nil {
			return e
		}
		if e = syncDir(base); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else if !st.IsDir() {
		return ErrMigrationRequired
	}
	if st, e := os.Lstat(legacy); errors.Is(e, os.ErrNotExist) {
		f, e := os.OpenFile(legacy, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		if _, e = f.WriteString("SQLite journal active; old binary disabled\n"); e != nil {
			f.Close()
			return e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
		if e = syncDir(base); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else if !st.Mode().IsRegular() {
		return fmt.Errorf("legacy path recreated by old binary; stop old processes before retrying migration")
	}
	entries, err := os.ReadDir(backup)
	if err != nil {
		return err
	}
	type item struct {
		profile string
		r       Record
	}
	var records []item
	nonces := map[string]int64{}
	var globalNonce int64
	var locks []*os.File
	defer func() {
		for _, f := range locks {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		}
	}()
	for _, entry := range entries {
		if !entry.IsDir() || !config.ValidName(entry.Name()) {
			return fmt.Errorf("unexpected legacy entry %q", entry.Name())
		}
		profile := entry.Name()
		dir := filepath.Join(backup, profile)
		f, e := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			f.Close()
			return fmt.Errorf("legacy profile %s active: %w", profile, e)
		}
		locks = append(locks, f)
		files, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		for _, file := range files {
			name := file.Name()
			p := filepath.Join(dir, name)
			if !file.Type().IsRegular() {
				return fmt.Errorf("unexpected legacy file %s", p)
			}
			switch {
			case name == ".lock":
			case strings.HasPrefix(name, ".tmp-"):
				// Old atomic writes leave non-authoritative temporary files on crash.
			case strings.HasPrefix(name, ".nonce-"):
				signer := strings.TrimPrefix(name, ".nonce-")
				if signer == "" {
					return fmt.Errorf("invalid legacy nonce name %s", p)
				}
				raw, e := os.ReadFile(p)
				if e != nil {
					return e
				}
				n, e := checkLegacyNonce(raw)
				if e != nil {
					return fmt.Errorf("%s: %w", p, e)
				}
				signer = strings.ToLower(signer)
				if n > nonces[signer] {
					nonces[signer] = n
				}
				if n > globalNonce {
					globalNonce = n
				}
			case strings.HasSuffix(name, ".json"):
				id := strings.TrimSuffix(name, ".json")
				if !trading.ValidIntentID(id) {
					return fmt.Errorf("invalid legacy intent %s", p)
				}
				raw, e := os.ReadFile(p)
				if e != nil {
					return e
				}
				var r Record
				if e = json.Unmarshal(raw, &r); e != nil {
					return fmt.Errorf("%s: %w", p, e)
				}
				if e = validateLegacyRecord(&r, id); e != nil {
					return fmt.Errorf("%s: %w", p, e)
				}
				records = append(records, item{profile, r})
				if r.Nonce > globalNonce {
					globalNonce = r.Nonce
				}
			default:
				return fmt.Errorf("unexpected legacy file %s", p)
			}
		}
	}
	db, err := openDB(base)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing int
	err = tx.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&existing)
	if err != nil {
		return err
	}
	var imported string
	importErr := tx.QueryRow(`SELECT value FROM migration_metadata WHERE key='legacy_import_count'`).Scan(&imported)
	if importErr != nil && !errors.Is(importErr, sql.ErrNoRows) {
		return importErr
	}
	if existing != 0 || importErr == nil {
		if importErr != nil || imported != fmt.Sprint(len(records)) {
			return errors.New("refusing legacy import into populated journal")
		}
		if err = tx.Rollback(); err != nil {
			return err
		}
	} else {
		for _, it := range records {
			r := it.r
			raw, _ := json.Marshal(r.Intent)
			_, err = tx.Exec(`INSERT INTO records(profile,intent_id,account,network,origin,signer,digest,intent_json,state,action_id,action_json,cloid,nonce,server_state,order_id,last_error,updated_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, it.profile, r.Intent.ID, strings.ToLower(r.Intent.Account), r.Intent.Network, r.Intent.APIURL, "", r.Digest, string(raw), r.State, r.ActionID, r.ActionJSON, r.Cloid, r.Nonce, r.ServerState, r.OrderID, r.LastError, r.UpdatedAt, r.Intent.CreatedAt)
			if err != nil {
				return err
			}
			for i, t := range r.History {
				_, err = tx.Exec(`INSERT INTO transitions(profile,intent_id,sequence,at,state,note) VALUES(?,?,?,?,?,?)`, it.profile, r.Intent.ID, i+1, t.At, t.State, t.Note)
				if err != nil {
					return err
				}
			}
		}
		for signer, n := range nonces {
			_, err = tx.Exec(`INSERT INTO nonces(signer,last_nonce) VALUES(?,?)`, signer, n)
			if err != nil {
				return err
			}
		}
		if globalNonce > 0 {
			_, err = tx.Exec(`INSERT INTO nonces(signer,last_nonce) VALUES('*',?)`, globalNonce)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(`INSERT INTO migration_metadata(key,value) VALUES('legacy_import_count',?),('legacy_import_at',?)`, fmt.Sprint(len(records)), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	// Verify every digest/state/history and nonce from a fresh read before switch.
	for _, it := range records {
		r, e := load(db, it.profile, it.r.Intent.ID)
		if e != nil {
			return e
		}
		if r.Digest != it.r.Digest || r.State != it.r.State || r.Nonce != it.r.Nonce || r.ActionID != it.r.ActionID || r.ActionJSON != it.r.ActionJSON || r.Cloid != it.r.Cloid || r.ServerState != it.r.ServerState || r.OrderID != it.r.OrderID || r.LastError != it.r.LastError || len(r.History) != len(it.r.History) {
			return errors.New("legacy import verification failed")
		}
		for i := range r.History {
			if r.History[i] != it.r.History[i] {
				return errors.New("legacy transition verification failed")
			}
		}
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&count); err != nil {
		return err
	}
	if count != len(records) {
		return errors.New("legacy import count mismatch")
	}
	for signer, n := range nonces {
		var got int64
		if err = db.QueryRow(`SELECT last_nonce FROM nonces WHERE signer=?`, signer).Scan(&got); err != nil {
			return err
		}
		if got != n {
			return errors.New("legacy nonce verification failed")
		}
	}
	if globalNonce > 0 {
		var got int64
		if err = db.QueryRow(`SELECT last_nonce FROM nonces WHERE signer='*'`).Scan(&got); err != nil {
			return err
		}
		if got != globalNonce {
			return errors.New("legacy global nonce verification failed")
		}
	}
	if err = os.Chmod(backup, 0700); err != nil {
		return err
	}
	if err = os.Remove(marker); err != nil {
		return err
	}
	return syncDir(base)
}
func validateLegacyRecord(r *Record, id string) error {
	if r.Intent.ID != id || r.Digest != r.Intent.Digest() {
		return errors.New("intent digest mismatch")
	}
	switch r.State {
	case StatePrepared, StateCreating, StateCreated, StateSubmitting, StateSubmitted, StateUnknown, StateRejected, StateAbandoned:
	default:
		return errors.New("invalid state")
	}
	if len(r.History) == 0 || r.History[len(r.History)-1].State != r.State {
		return errors.New("invalid transition history")
	}
	if (r.State == StateSubmitting || r.State == StateUnknown) && r.Nonce <= 0 {
		return errors.New("unresolved intent missing nonce")
	}
	if r.Nonce < 0 {
		return errors.New("negative nonce")
	}
	if r.UpdatedAt == "" {
		return errors.New("missing updated time")
	}
	return nil
}
