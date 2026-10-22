package journal

import (
	"context"
	"errors"
	"github.com/pressly/goose/v3"
	"io/fs"
	"strings"
	"testing"
)

func TestClaimMigrationPreservesExistingIntents(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	j, err := Open("first")
	if err != nil {
		t.Fatal(err)
	}
	in := OnchainIntent{ID: "legacy-bridge", Kind: "bridge", ChainID: 42161, Sender: "0x1111111111111111111111111111111111111111", Recipient: "0x2222222222222222222222222222222222222222", Token: "native", Amount: "1", Templates: []OnchainTemplate{{ChainID: 42161, To: "0x2222222222222222222222222222222222222222", Value: "0x0"}}}
	if _, err = j.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	providerFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, j.db, providerFS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.DownTo(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if _, err = j.GetOnchain(in.ID); err != nil {
		t.Fatalf("lost existing intent: %v", err)
	}
	in.ID = "new-claim"
	in.Kind = "claim"
	in.ParentIntent = "legacy-bridge"
	if _, err = j.CreateOnchain(in); err != nil {
		t.Fatalf("claim constraint not migrated: %v", err)
	}
}

func TestSequentialCursorSurvivesReopen(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	j, err := Open("first")
	if err != nil {
		t.Fatal(err)
	}
	in := OnchainIntent{ID: "sequential-resume", Kind: "swap", ChainID: 30, Sender: "0x1111111111111111111111111111111111111111", Recipient: "0x2222222222222222222222222222222222222222", Token: "native", Amount: "1", Templates: []OnchainTemplate{{ChainID: 30, To: "0x2222222222222222222222222222222222222222", Value: "0x0"}, {ChainID: 30, To: "0x2222222222222222222222222222222222222222", Value: "0x0"}}}
	if _, err := j.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginOnchain(in.ID, in.Digest(), in.Sender); err != nil {
		t.Fatal(err)
	}
	if err := j.SetExecutionMode(in.ID, "sequential"); err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("a", 64)
	if err := j.AdvanceOnchain(in.ID, "sending", "sent", hash); err != nil {
		t.Fatal(err)
	}
	if err := j.AdvanceOnchain(in.ID, "sent", "sending", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	r, err := j.GetOnchain(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "sending" || r.StepIndex != 1 || r.NextStep != "step 1 confirmed; step 2 not sent" || len(r.Hashes) != 1 || r.Hashes[0] != hash {
		t.Fatalf("cursor lost: %+v", r)
	}
	if err := j.MarkStepHandoff(in.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open("first")
	if err != nil {
		t.Fatal(err)
	}
	r, err = j.GetOnchain(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.StepInFlight || !strings.Contains(r.NextStep, "ambiguous") {
		t.Fatalf("unsafe resume after handoff: %+v", r)
	}
}

func TestOnchainUnknownBlocksAcrossProfiles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	first, err := Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	in := OnchainIntent{ID: "intent-one", Kind: "send", ChainID: 42161, Sender: "0x1111111111111111111111111111111111111111", Recipient: "0x2222222222222222222222222222222222222222", Token: "native", Amount: "1", Templates: []OnchainTemplate{{ChainID: 42161, To: "0x2222222222222222222222222222222222222222", Value: "0x1"}}}
	if _, err = first.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	if err = first.BeginOnchain(in.ID, in.Digest(), in.Sender); err != nil {
		t.Fatal(err)
	}
	if err = first.AdvanceOnchain(in.ID, "sending", "unknown", ""); err != nil {
		t.Fatal(err)
	}
	second, err := Open("second")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	in.ID = "intent-two"
	if _, err = second.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	if err = second.BeginOnchain(in.ID, in.Digest(), in.Sender); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("expected unresolved gate: %v", err)
	}
}
func TestOnchainDigestMismatchCannotStart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	j, err := Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	in := OnchainIntent{ID: "intent-one", Kind: "send", ChainID: 1, Sender: "0x1111111111111111111111111111111111111111", Recipient: "0x2222222222222222222222222222222222222222", Token: "native", Amount: "1", Templates: []OnchainTemplate{{ChainID: 1, To: "0x2222222222222222222222222222222222222222", Value: "0x1"}}}
	if _, err = j.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(in.ID, "wrong", in.Sender); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected digest rejection: %v", err)
	}
}
