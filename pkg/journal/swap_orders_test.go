package journal

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

func swapOrder(id string) SwapOrder {
	return SwapOrder{ID: id, Agent: "0x1111111111111111111111111111111111111111", ChainID: 42161, Token: "0x912CE59144191C1204E64559FE8253a0e49E6548", Decimals: 18, Amount: "all", Size: "100", StopLossPct: "10", SlippageBps: 50, Basis: "0.01", BasisSource: "override"}
}

func TestSwapOrderMigrationFromV7(t *testing.T) {
	j := open(t)
	in := OnchainIntent{ID: "legacy-swap", Kind: "swap", ChainID: 42161, Sender: "0x1111111111111111111111111111111111111111", Recipient: "0x1111111111111111111111111111111111111111", Token: "native", Amount: "1", Templates: []OnchainTemplate{{ChainID: 42161, To: "0x2222222222222222222222222222222222222222", Value: "0x0"}}}
	if _, err := j.CreateOnchain(in); err != nil {
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
	if _, err = provider.DownTo(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if _, err = j.ListSwapOrders(); err == nil {
		t.Fatal("swap_orders survived down migration")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open("p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if _, err = j.GetOnchain(in.ID); err != nil {
		t.Fatalf("lost existing intent: %v", err)
	}
	if _, err = j.CreateSwapOrder(swapOrder("after-upgrade")); err != nil {
		t.Fatalf("swap_orders not migrated: %v", err)
	}
}

func TestSwapOrderCRUD(t *testing.T) {
	j := open(t)
	o, err := j.CreateSwapOrder(swapOrder("so-1"))
	if err != nil || o.State != SwapOrderActive || o.Agent != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("create: %+v %v", o, err)
	}
	if _, err = j.CreateSwapOrder(swapOrder("so-1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate id: %v", err)
	}
	none := swapOrder("so-none")
	none.StopLossPct = ""
	if _, err = j.CreateSwapOrder(none); err == nil {
		t.Fatal("order without a trigger accepted")
	}
	if _, err = j.CreateSwapOrder(swapOrder("so-2")); err != nil {
		t.Fatal(err)
	}
	got, err := j.GetSwapOrder("so-1")
	if err != nil || got.Size != "100" || got.BasisSource != "override" || got.StopLossPct != "10" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err = j.GetSwapOrder("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err = j.TransitionSwapOrder("so-2", SwapOrderActive, SwapOrderCancelled, SwapOrderPatch{}); err != nil {
		t.Fatal(err)
	}
	all, err := j.ListSwapOrders()
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %d %v", len(all), err)
	}
	active, err := j.ListSwapOrders(SwapOrderActive)
	if err != nil || len(active) != 1 || active[0].ID != "so-1" {
		t.Fatalf("list active: %+v %v", active, err)
	}
	other, err := Open("other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if mine, _ := other.ListSwapOrders(); len(mine) != 0 {
		t.Fatal("orders leaked across profiles")
	}
}

func TestSwapOrderTransitionsAreCompareAndSet(t *testing.T) {
	j := open(t)
	if _, err := j.CreateSwapOrder(swapOrder("so-cas")); err != nil {
		t.Fatal(err)
	}
	trigger := "stop_loss"
	if err := j.TransitionSwapOrder("so-cas", SwapOrderActive, SwapOrderTriggered, SwapOrderPatch{Trigger: &trigger}); err != nil {
		t.Fatal(err)
	}
	// A second watcher that saw the order active loses the race.
	if err := j.TransitionSwapOrder("so-cas", SwapOrderActive, SwapOrderTriggered, SwapOrderPatch{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("double trigger: %v", err)
	}
	intent := "sell-1"
	if err := j.TransitionSwapOrder("so-cas", SwapOrderTriggered, SwapOrderExecuting, SwapOrderPatch{SellIntentID: &intent}); err != nil {
		t.Fatal(err)
	}
	if err := j.TransitionSwapOrder("so-cas", SwapOrderExecuting, SwapOrderCancelled, SwapOrderPatch{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel while executing: %v", err)
	}
	if err := j.TransitionSwapOrder("so-cas", SwapOrderActive, SwapOrderCancelled, SwapOrderPatch{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cancel: %v", err)
	}
	reconcile, msg := true, "outcome unknown"
	if err := j.TransitionSwapOrder("so-cas", SwapOrderExecuting, SwapOrderFailed, SwapOrderPatch{NeedsReconcile: &reconcile, LastError: &msg}); err != nil {
		t.Fatal(err)
	}
	if err := j.TransitionSwapOrder("so-cas", SwapOrderFailed, SwapOrderActive, SwapOrderPatch{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("failed order revived: %v", err)
	}
	got, _ := j.GetSwapOrder("so-cas")
	if got.State != SwapOrderFailed || !got.NeedsReconcile || got.SellIntentID != "sell-1" || got.Trigger != "stop_loss" || got.LastError != msg {
		t.Fatalf("final: %+v", got)
	}
}

func TestSwapOrderPeakRatchetsUpOnly(t *testing.T) {
	j := open(t)
	if _, err := j.CreateSwapOrder(swapOrder("so-peak")); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ value, peak string }{{"1.5", "1.5"}, {"2", "2"}, {"1.2", "2"}, {"2.25", "2.25"}, {"0.1", "2.25"}} {
		peak, err := j.RecordSwapOrderQuote("so-peak", "0.01", step.value, "t")
		if err != nil || peak != step.peak {
			t.Fatalf("value %s: peak %s want %s (%v)", step.value, peak, step.peak, err)
		}
	}
	got, _ := j.GetSwapOrder("so-peak")
	if got.PeakValue != "2.25" || got.LastValue != "0.1" || got.LastPrice != "0.01" {
		t.Fatalf("stored: %+v", got)
	}
	if err := j.TransitionSwapOrder("so-peak", SwapOrderActive, SwapOrderCancelled, SwapOrderPatch{}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RecordSwapOrderQuote("so-peak", "0.01", "9", "t"); !errors.Is(err, ErrConflict) {
		t.Fatalf("quote on cancelled order: %v", err)
	}
}

func TestListOnchainSwapsConfirmedOnly(t *testing.T) {
	j := open(t)
	agent := "0x1111111111111111111111111111111111111111"
	for _, id := range []string{"buy-ok", "buy-pending"} {
		in := OnchainIntent{ID: id, Kind: "swap", ChainID: 42161, Sender: agent, Recipient: agent, Token: "native", OutToken: "0x912CE59144191C1204E64559FE8253a0e49E6548", Amount: "1000", MinOut: "5", Templates: []OnchainTemplate{{ChainID: 42161, To: "0x2222222222222222222222222222222222222222", Value: "0x0"}}}
		if _, err := j.CreateOnchain(in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.db.Exec(`UPDATE onchain_intents SET state='confirmed' WHERE intent_id='buy-ok'`); err != nil {
		t.Fatal(err)
	}
	got, err := j.ListOnchainSwaps(agent, 42161)
	if err != nil || len(got) != 1 || got[0].ID != "buy-ok" {
		t.Fatalf("swaps: %+v %v", got, err)
	}
	if other, _ := j.ListOnchainSwaps(agent, 8453); len(other) != 0 {
		t.Fatal("other chain included")
	}
}
