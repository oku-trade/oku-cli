package cli

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
	v1 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func walletBalancesFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID (default 42161)"}, &urfave.StringFlag{Name: "chains", Usage: "comma-separated EVM chain IDs"}, &urfave.BoolFlag{Name: "all-chains", Usage: "query common supported EVM chains"}}
}
func depositAddressFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "asset", Usage: "HyperUnit asset symbol"}, &urfave.StringFlag{Name: "source-chain", Usage: "HyperUnit source chain CAIP-2 identifier"}}
}
func agentWalletMetadata(e *env) (*resolvedWallet, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" || !config.ValidAddress(p.Account) || e.g.account != "" && !strings.EqualFold(e.g.account, p.Account) {
		return nil, output.Errorf(output.CodeAccountRequired, "select the enrolled master profile; --account cannot override the signer")
	}
	md, err := auth.LoadPrivyMetadata(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "loading agent wallet metadata")
	}
	if !strings.EqualFold(md.AccountAddress, p.Account) || !config.ValidAddress(md.AgentAddress) || strings.EqualFold(md.AgentAddress, p.Account) || md.PrivyWalletID == "" {
		return nil, output.Errorf(output.CodeSignerUnavailable, "agent wallet is not bound to the selected master profile")
	}
	return &resolvedWallet{profile: p, agent: md.AgentAddress}, nil
}

type resolvedWallet struct {
	profile *resolved
	agent   string
}

func (a *App) cmdWalletAddress(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"agent": w.agent, "master": w.profile.Account, "profile": w.profile.Name}
	return &result{data: data, human: func(out io.Writer) {
		fmt.Fprintf(out, "Agent EVM wallet: %s\nMaster EOA: %s\n", w.agent, w.profile.Account)
	}}, nil
}
func (a *App) cmdWalletBalances(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	if (e.cmd.Bool("all-chains") && (e.cmd.String("chain") != "" || e.cmd.String("chains") != "")) || (e.cmd.String("chain") != "" && e.cmd.String("chains") != "") {
		return nil, output.Errorf(output.CodeInvalidArgument, "choose one of --chain, --chains or --all-chains")
	}
	selected := e.cmd.String("chain")
	if e.cmd.String("chains") != "" {
		selected = e.cmd.String("chains")
	}
	if e.cmd.Bool("all-chains") {
		selected = "1,10,56,137,8453,42161,43114,81457"
	}
	if selected == "" {
		selected = "42161"
	}
	chains := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(selected, ",") {
		id, perr := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(part), "eip155:"), 10, 64)
		if perr != nil || !onchain.Allowed(id) {
			return nil, output.Errorf(output.CodeInvalidArgument, "unsupported EVM chain %q", part)
		}
		chain := fmt.Sprintf("eip155:%d", id)
		if !seen[chain] {
			chains = append(chains, chain)
			seen[chain] = true
		}
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	client := e.client(w.profile)
	holdings := &v1.AddressHoldingsResponse{}
	for _, chain := range chains {
		perChain := &v1.AddressHoldingsResponse{}
		if err = client.AddressHoldings(ctx, holdingsRequest(w.agent, chain), perChain); err != nil {
			return nil, apiError(err, "AddressHoldings")
		}
		id, _ := strconv.ParseUint(strings.TrimPrefix(chain, "eip155:"), 10, 64)
		for _, holding := range perChain.Balances {
			// Production responses predate chain and chain_id fields.
			if holding.ChainId == 0 {
				holding.ChainId = int64(id)
			}
			if holding.Chain == "" {
				holding.Chain = chain
			}
		}
		holdings.Balances = append(holdings.Balances, perChain.Balances...)
		holdings.UnsupportedChains = append(holdings.UnsupportedChains, perChain.UnsupportedChains...)
	}
	native := map[string]any{}
	onchainTokens := map[string]any{}
	for _, chain := range chains {
		var balance map[string]any
		if err = client.Onchain(ctx, "GetNativeBalance", map[string]any{"chain": chain, "address": w.agent}, &balance, false); err != nil {
			return nil, apiError(err, "GetNativeBalance")
		}
		native[chain] = balance
		var fresh map[string]any
		id, _ := strconv.ParseUint(strings.TrimPrefix(chain, "eip155:"), 10, 64)
		identifiers := []map[string]any{}
		for _, holding := range holdings.Balances {
			if holding.ChainId == int64(id) && id <= math.MaxInt32 && onchain.ValidAddress(holding.TokenAddress) {
				identifiers = append(identifiers, map[string]any{"address": holding.TokenAddress, "chainId": id})
			}
		}
		freshBalances := []any{}
		for offset := 0; offset < len(identifiers) || offset == 0; offset += 100 {
			end := offset + 100
			if end > len(identifiers) {
				end = len(identifiers)
			}
			request := map[string]any{"walletAddress": w.agent, "tokenIdentifiers": identifiers[offset:end]}
			if offset == 0 {
				request["includeRecommendedDefaultToken"] = true
				request["recommendChains"] = []string{chain}
			}
			if err = client.Onchain(ctx, "GetOnchainTokenBalances", request, &fresh, false); err != nil {
				return nil, apiError(err, "GetOnchainTokenBalances")
			}
			if rows, ok := fresh["balances"].([]any); ok {
				freshBalances = append(freshBalances, rows...)
			}
		}
		fresh["balances"] = freshBalances
		onchainTokens[chain] = fresh
	}
	data := map[string]any{"agent": w.agent, "chains": chains, "native": native, "holdings": holdings, "onchain_tokens": onchainTokens, "source": "oku:WalletService.AddressHoldings + OnchainService"}
	return &result{data: data, human: func(out io.Writer) {
		fmt.Fprintf(out, "Agent %s\n", w.agent)
		for _, chain := range chains {
			fmt.Fprintf(out, "%s native: %v\n", chain, native[chain])
		}
		for _, b := range holdings.Balances {
			fmt.Fprintf(out, "%s %s (%s): indexed %s\n", b.Chain, b.Symbol, b.TokenAddress, b.Balance)
		}
		for _, chain := range chains {
			if fresh, ok := onchainTokens[chain].(map[string]any); ok {
				if rows, ok := fresh["balances"].([]any); ok {
					for _, row := range rows {
						fmt.Fprintf(out, "%s onchain: %v\n", chain, row)
					}
				}
			}
		}
		for _, chain := range holdings.UnsupportedChains {
			fmt.Fprintf(out, "Unsupported: %s\n", chain)
		}
	}}, nil
}

// The deployed backend predates AddressHoldingsRequest.chains. Use its legacy
// singular chain field for each selected chain rather than relying on repeated
// chains being recognized by the server.
func holdingsRequest(address, chain string) *v1.AddressHoldingsRequest {
	return &v1.AddressHoldingsRequest{Address: address, Chain: chain}
}
func (a *App) cmdDepositAddress(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	asset := strings.ToLower(strings.TrimSpace(e.cmd.String("asset")))
	chain := strings.TrimSpace(e.cmd.String("source-chain"))
	if asset == "" || chain == "" {
		return nil, output.Errorf(output.CodeInvalidArgument, "--asset and --source-chain are required")
	}
	if strings.EqualFold(w.profile.Account, w.agent) {
		return nil, output.Errorf(output.CodeInvalidArgument, "Hyperliquid deposit beneficiary cannot be the agent")
	}
	var response struct {
		DepositAddress string `json:"depositAddress"`
		HLAddress      string `json:"hlAddress"`
		MinDeposit     string `json:"minDeposit"`
		Asset          string `json:"asset"`
		SourceChain    string `json:"sourceChain"`
	}
	if err = e.perpsRead(w.profile, "GenerateDepositAddress", map[string]any{"hlAddress": w.profile.Account, "asset": asset, "sourceChain": chain}, &response); err != nil {
		return nil, err
	}
	validDeposit := false
	switch {
	case strings.HasPrefix(chain, "eip155:"):
		validDeposit = onchain.ValidAddress(response.DepositAddress)
	case strings.HasPrefix(chain, "solana:"):
		validDeposit = validBase58Address(response.DepositAddress)
	case strings.HasPrefix(chain, "bip122:"):
		validDeposit = validBitcoinAddress(response.DepositAddress)
	}
	if !strings.EqualFold(response.HLAddress, w.profile.Account) || !strings.EqualFold(response.Asset, asset) || !validDeposit || response.SourceChain != chain {
		return nil, output.Errorf(output.CodeInvalidArgument, "deposit response does not bind address to master and requested chain")
	}
	return &result{data: response, human: func(out io.Writer) {
		fmt.Fprintf(out, "Master-bound %s deposit address (%s): %s\nMinimum: %s\nFund externally from your own wallet; agent transfers do not credit the master.\n", asset, chain, response.DepositAddress, response.MinDeposit)
	}}, nil
}

func validBase58Address(s string) bool {
	if len(s) < 32 || len(s) > 44 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz", c) {
			return false
		}
	}
	return true
}

func validBitcoinAddress(s string) bool {
	if len(s) >= 14 && len(s) <= 90 && (strings.HasPrefix(s, "bc1q") || strings.HasPrefix(s, "bc1p")) {
		for _, c := range s[4:] {
			if !strings.ContainsRune("023456789acdefghjklmnpqrstuvwxyz", c) {
				return false
			}
		}
		return true
	}
	return len(s) >= 26 && len(s) <= 35 && (strings.HasPrefix(s, "1") || strings.HasPrefix(s, "3")) && validBase58Alphabet(s)
}

func validBase58Alphabet(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz", c) {
			return false
		}
	}
	return true
}
