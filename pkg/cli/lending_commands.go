package cli

import urfave "github.com/urfave/cli/v3"

// lendingCommands is the earn, borrow and loop command table. Each handler
// carries its own required flags and enums so the feature stays in one
// place, like lpCommands.
func (a *App) lendingCommands() map[string]*handler {
	req := func(names ...string) map[string]bool {
		m := make(map[string]bool, len(names))
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	proto := map[string][]string{"protocol": {protoMorpho, protoEuler}}
	unwind := map[string][]string{"protocol": {protoMorpho, protoEuler}, "to": {"collateral", "loan"}}
	submit := func(kind string) func() []urfave.Flag {
		return func() []urfave.Flag { return submitFlagsFor(kind + " prepare") }
	}
	open := func() []urfave.Flag { return loopFlags(true) }
	change := func() []urfave.Flag { return loopFlags(false) }
	return map[string]*handler{
		"earn vaults":    {summary: "List Morpho or Euler earn vaults with APY and TVL (read-only)", flags: earnVaultsFlags, run: a.cmdEarnVaults, required: req("chain", "protocol"), enums: proto},
		"earn positions": {summary: "List the agent's earn vault positions (read-only)", flags: lendingTargetFlags, run: a.cmdEarnPositions, required: req("chain", "protocol"), enums: proto},
		"earn prepare":   {summary: "Prepare a vault deposit or withdrawal (never signs)", flags: earnPrepareFlags, run: a.cmdEarnPrepare, required: req("chain", "protocol", "vault"), enums: proto},
		"earn submit":    {summary: "Submit a verified earn intent", flags: submit("earn"), run: a.cmdEarnSubmit, required: req("intent", "digest")},
		"earn status":    {summary: "Reconcile an earn intent", flags: statusFlags, run: a.cmdEarnStatus, required: req("intent")},
		"earn reconcile": {summary: "Attach and verify a recovered earn transaction hash", flags: reconcileFlags, run: a.cmdEarnReconcile, required: req("intent", "tx-hash")},

		"borrow markets":   {summary: "List Morpho or Euler borrow markets with LLTV, rates and liquidity (read-only)", flags: borrowMarketsFlags, run: a.cmdBorrowMarkets, required: req("chain", "protocol"), enums: proto},
		"borrow positions": {summary: "List the agent's borrow positions with LTV, health factor and liquidation price (read-only)", flags: lendingTargetFlags, run: a.cmdBorrowPositions, required: req("chain", "protocol"), enums: proto},
		"borrow prepare":   {summary: "Prepare supply/borrow or repay/withdraw on a market, refused above the max LTV (never signs)", flags: borrowPrepareFlags, run: a.cmdBorrowPrepare, required: req("chain", "protocol"), enums: proto},
		"borrow submit":    {summary: "Submit a verified borrow intent", flags: submit("borrow"), run: a.cmdBorrowSubmit, required: req("intent", "digest")},
		"borrow status":    {summary: "Reconcile a borrow intent", flags: statusFlags, run: a.cmdBorrowStatus, required: req("intent")},
		"borrow reconcile": {summary: "Attach and verify a recovered borrow transaction hash", flags: reconcileFlags, run: a.cmdBorrowReconcile, required: req("intent", "tx-hash")},

		"loop open":      {summary: "Prepare a leveraged loop: deposit, flash borrow, swap to collateral, supply, in one transaction (never signs)", flags: open, run: a.cmdLoopOpen, required: req("chain", "protocol", "deposit", "target-leverage"), enums: proto},
		"loop adjust":    {summary: "Prepare an atomic leverage increase or decrease to a target leverage or LTV (never signs)", flags: change, run: a.cmdLoopAdjust, required: req("chain", "protocol"), enums: unwind},
		"loop unwind":    {summary: "Prepare closing a loop: repay all debt from collateral sold in the same transaction (never signs)", flags: change, run: a.cmdLoopUnwind, required: req("chain", "protocol"), enums: unwind},
		"loop submit":    {summary: "Submit a verified loop intent", flags: submit("loop"), run: a.cmdLoopSubmit, required: req("intent", "digest")},
		"loop status":    {summary: "Reconcile a loop intent", flags: statusFlags, run: a.cmdLoopStatus, required: req("intent")},
		"loop reconcile": {summary: "Attach and verify a recovered loop transaction hash", flags: reconcileFlags, run: a.cmdLoopReconcile, required: req("intent", "tx-hash")},
	}
}
