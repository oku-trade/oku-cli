package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func walletBalancesFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID (default 42161)"}, &urfave.StringFlag{Name: "chains", Usage: "comma-separated EVM chain IDs"}, &urfave.BoolFlag{Name: "all-chains", Usage: "query common supported EVM chains"}, &urfave.StringFlag{Name: "tokens-file", Usage: "CSV token manifest (chain_id,token_address,amount); native rows use the zero address; reads the CSV chains when --chain/--chains is omitted"}, &urfave.BoolFlag{Name: "stables", Usage: "also read the chain's configured stablecoins from chain-config (intermediate-asset candidates)"}}
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
func (a *App) cmdWalletBalances(e *env) (*result, error) { return a.walletBalancesNative(e) }

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
