package journal

import (
	"context"
	"errors"
	"github.com/pressly/goose/v3"
	"io/fs"
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
	defer j.Close()
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
