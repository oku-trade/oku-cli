package cli

import urfave "github.com/urfave/cli/v3"

// lpCommands is the Uniswap liquidity command table. Each handler carries
// its own required flags and enums so the feature stays in one place.
func (a *App) lpCommands() map[string]*handler {
	req := func(names ...string) map[string]bool {
		m := make(map[string]bool, len(names))
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	version := map[string][]string{"version": {"v3", "v4"}}
	submit := func() []urfave.Flag { return submitFlagsFor("lp <action>") }
	return map[string]*handler{
		"lp pool":             {summary: "Read a Uniswap v3/v4 pool: price, tick, liquidity (read-only)", flags: lpPoolFlags, run: a.cmdLPPool, required: req("chain", "token0", "token1", "fee"), enums: version},
		"lp position":         {summary: "Read one Uniswap v3/v4 position: range, amounts, uncollected fees (read-only)", flags: lpPositionFlags, run: a.cmdLPPosition, required: req("chain", "token-id"), enums: version},
		"lp list":             {summary: "List the agent's Uniswap v3/v4 positions on a chain (read-only)", flags: lpListFlags, run: a.cmdLPList, required: req("chain"), enums: version},
		"lp open prepare":     {summary: "Prepare a new Uniswap v3/v4 position (never signs)", flags: lpOpenFlags, run: a.cmdLPOpenPrepare, required: req("chain", "token0", "token1", "fee", "amount", "amount-token"), enums: version},
		"lp increase prepare": {summary: "Prepare adding liquidity to a position (never signs)", flags: lpIncreaseFlags, run: a.cmdLPIncreasePrepare, required: req("chain", "token-id", "amount", "amount-token"), enums: version},
		"lp decrease prepare": {summary: "Prepare removing a percent of a position's liquidity, collecting the tokens and fees (never signs)", flags: lpDecreaseFlags, run: a.cmdLPDecreasePrepare, required: req("chain", "token-id", "percent"), enums: version},
		"lp collect prepare":  {summary: "Prepare collecting a position's uncollected fees (never signs)", flags: lpCollectFlags, run: a.cmdLPCollectPrepare, required: req("chain", "token-id"), enums: version},
		"lp close prepare":    {summary: "Prepare removing all liquidity and fees and burning the position NFT (never signs)", flags: lpCloseFlags, run: a.cmdLPClosePrepare, required: req("chain", "token-id"), enums: version},
		"lp submit":           {summary: "Submit a verified lp intent (approvals and the position action)", flags: submit, run: a.cmdLPSubmit, required: req("intent", "digest")},
		"lp status":           {summary: "Reconcile an lp intent", flags: statusFlags, run: a.cmdLPStatus, required: req("intent")},
		"lp reconcile":        {summary: "Attach and verify a recovered lp transaction hash", flags: reconcileFlags, run: a.cmdLPReconcile, required: req("intent", "tx-hash")},
	}
}
