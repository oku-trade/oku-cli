package cli

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gfx-labs/chain-config/dist/networks"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// chainStables returns the chain's configured stablecoins from chain-config
// (for example USDC.e on 0G), used as intermediate-asset candidates.
func chainStables(chain uint64) []string {
	n, err := networks.NetworkByNumber(chain)
	if err != nil || n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Stables))
	for _, a := range n.Stables {
		out = append(out, a.Hex())
	}
	return out
}

// allOkuChains lists every non-deprecated EVM chain in chain-config that has
// an Oku Router, as a comma-separated chain ID list.
func allOkuChains() string {
	ids := []string{}
	for _, n := range networks.AllNetworks {
		if n.IsEVM() && !n.Deprecated && n.Oku.Router != (common.Address{}) {
			ids = append(ids, strconv.FormatUint(uint64(n.ChainId), 10))
		}
	}
	return strings.Join(ids, ",")
}

// nativeSymbol returns the chain's native currency symbol from chain-config.
func nativeSymbol(chain uint64) string {
	if n, err := networks.NetworkByNumber(chain); err == nil && n != nil && n.NativeCurrency.Symbol != "" {
		return n.NativeCurrency.Symbol
	}
	return "native"
}

type manifestToken struct {
	Address string
	Amount  string
}

func parseTokenManifest(reader io.Reader) (map[uint64][]manifestToken, error) {
	parser := csv.NewReader(reader)
	parser.FieldsPerRecord = -1
	records, err := parser.ReadAll()
	if err != nil {
		return nil, err
	}
	out := map[uint64][]manifestToken{}
	seen := map[string]bool{}
	for index, row := range records {
		if index == 0 && len(row) > 1 && strings.EqualFold(strings.TrimSpace(row[0]), "chain_id") && strings.EqualFold(strings.TrimSpace(row[1]), "token_address") {
			continue
		}
		if len(row) < 2 {
			return nil, fmt.Errorf("CSV row %d needs chain_id,token_address", index+1)
		}
		id, er := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(row[0]), "eip155:"), 10, 64)
		if er != nil || !onchain.Allowed(id) {
			return nil, fmt.Errorf("invalid chain at CSV row %d", index+1)
		}
		// The sweep exports native fee rows as the zero address (0xEeee also
		// accepted); both mean the chain's native asset.
		address := normalizeToken(row[1])
		if address != "native" && !onchain.ValidAddress(address) {
			return nil, fmt.Errorf("invalid token address at CSV row %d", index+1)
		}
		key := fmt.Sprintf("%d:%s", id, strings.ToLower(address))
		if seen[key] {
			continue
		}
		seen[key] = true
		amount := ""
		if len(row) > 2 {
			amount = strings.TrimSpace(row[2])
		}
		out[id] = append(out[id], manifestToken{address, amount})
	}
	return out, nil
}

func (a *App) walletBalancesNative(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	manifest := map[uint64][]manifestToken{}
	if path := e.cmd.String("tokens-file"); path != "" {
		file, er := os.Open(path)
		if er != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, er, "opening tokens file")
		}
		defer file.Close()
		manifest, er = parseTokenManifest(file)
		if er != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, er, "reading tokens file")
		}
	}
	selected := e.cmd.String("chain")
	if e.cmd.String("chains") != "" {
		selected = e.cmd.String("chains")
	}
	if e.cmd.Bool("all-chains") {
		selected = allOkuChains()
	}
	if selected == "" && len(manifest) > 0 {
		// Without an explicit chain selection, read every chain in the CSV.
		csvChains := make([]uint64, 0, len(manifest))
		for id := range manifest {
			csvChains = append(csvChains, id)
		}
		sort.Slice(csvChains, func(i, j int) bool { return csvChains[i] < csvChains[j] })
		parts := make([]string, len(csvChains))
		for i, id := range csvChains {
			parts[i] = strconv.FormatUint(id, 10)
		}
		selected = strings.Join(parts, ",")
	}
	if selected == "" {
		selected = "42161"
	}
	if (e.cmd.Bool("all-chains") && (e.cmd.String("chain") != "" || e.cmd.String("chains") != "")) || (e.cmd.String("chain") != "" && e.cmd.String("chains") != "") {
		return nil, output.Errorf(output.CodeInvalidArgument, "choose one of --chain, --chains or --all-chains")
	}
	ids := []uint64{}
	seen := map[uint64]bool{}
	for _, part := range strings.Split(selected, ",") {
		id, er := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(part), "eip155:"), 10, 64)
		if er != nil || !onchain.Allowed(id) {
			return nil, output.Errorf(output.CodeInvalidArgument, "invalid chain %q", part)
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	type row struct {
		Chain     string `json:"chain"`
		Address   string `json:"address"`
		Symbol    string `json:"symbol"`
		Decimals  int    `json:"decimals"`
		Balance   string `json:"balance"`
		Raw       string `json:"raw"`
		CSVAmount string `json:"csv_amount,omitempty"`
		Mismatch  bool   `json:"mismatch"`
		Stable    bool   `json:"stable,omitempty"`
	}
	type chainResult struct {
		native row
		tokens []row
		err    error
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	// Each chain is read independently so one unreachable or unconfigured
	// chain never hides the balances of the others.
	readChain := func(id uint64) chainResult {
		chain := fmt.Sprintf("eip155:%d", id)
		balance, er := rpc.Balance(ctx, id, w.agent)
		if er != nil {
			return chainResult{err: fmt.Errorf("native balance: %w", er)}
		}
		nativeRow := row{Chain: chain, Address: zeroAddress, Symbol: nativeSymbol(id), Decimals: 18, Balance: formatUnits(balance, 18), Raw: balance.String()}
		candidates := manifest[id]
		addresses := make([]string, 0, len(candidates))
		for _, c := range candidates {
			if c.Address == "native" {
				// Native CSV rows compare against the native balance, not a multicall.
				nativeRow.CSVAmount = c.Amount
				nativeRow.Mismatch = c.Amount != "" && c.Amount != nativeRow.Balance
				continue
			}
			addresses = append(addresses, c.Address)
		}
		stables := chainStables(id)
		if e.cmd.Bool("stables") {
			for _, st := range stables {
				dup := false
				for _, a := range addresses {
					dup = dup || strings.EqualFold(a, st)
				}
				if !dup {
					addresses = append(addresses, st)
				}
			}
		}
		details, er := rpc.TokenDetails(ctx, id, w.agent, addresses)
		if er != nil {
			return chainResult{err: fmt.Errorf("multicall token metadata: %w", er)}
		}
		rows := []row{}
		for _, d := range details {
			csvAmount := ""
			for _, c := range candidates {
				if strings.EqualFold(c.Address, d.Address) {
					csvAmount = c.Amount
					break
				}
			}
			actual := formatUnits(d.Balance, d.Decimals)
			stable := false
			for _, st := range stables {
				stable = stable || strings.EqualFold(st, d.Address)
			}
			rows = append(rows, row{Chain: chain, Address: d.Address, Symbol: d.Symbol, Decimals: d.Decimals, Balance: actual, Raw: d.Balance.String(), CSVAmount: csvAmount, Mismatch: csvAmount != "" && csvAmount != actual, Stable: stable})
		}
		return chainResult{native: nativeRow, tokens: rows}
	}
	results := make([]chainResult, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = readChain(id)
		}()
	}
	wg.Wait()
	type chainError struct {
		Chain   string `json:"chain"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	tokens := []row{}
	native := map[string]row{}
	chains := []string{}
	failed := []chainError{}
	for i, id := range ids {
		chain := fmt.Sprintf("eip155:%d", id)
		r := results[i]
		if r.err != nil {
			code := "UPSTREAM_ERROR"
			if errors.Is(r.err, onchain.ErrNoRPC) {
				code = "NO_RPC"
			}
			failed = append(failed, chainError{Chain: chain, Code: code, Message: r.err.Error()})
			continue
		}
		chains = append(chains, chain)
		native[chain] = r.native
		tokens = append(tokens, r.tokens...)
	}
	if len(chains) == 0 {
		return nil, output.Errorf(output.CodeUpstream, "no chain could be read: %s", failed[0].Message).WithDetail("errors", failed)
	}
	hint := ""
	if e.cmd.String("tokens-file") == "" {
		hint = "Provide --tokens-file FILE to check ERC-20 holdings against a CSV manifest."
	}
	data := map[string]any{"agent": w.agent, "chains": chains, "native": native, "tokens": tokens}
	if len(failed) > 0 {
		data["errors"] = failed
	}
	if hint != "" {
		data["hint"] = hint
	}
	return &result{data: data, human: func(out io.Writer) {
		fmt.Fprintf(out, "Agent %s\n\n", w.agent)
		withCSV := e.cmd.String("tokens-file") != ""
		tw := newTable(out)
		if withCSV {
			fmt.Fprintln(tw, "CHAIN\tID\tASSET\tBALANCE\tCSV\tMISMATCH\tADDRESS")
		} else {
			fmt.Fprintln(tw, "CHAIN\tID\tASSET\tBALANCE\tADDRESS")
		}
		line := func(r row) {
			id := strings.TrimPrefix(r.Chain, "eip155:")
			name := id
			if n, err := strconv.ParseUint(id, 10, 64); err == nil {
				name = chainName(n)
			}
			addr := r.Address
			if addr == zeroAddress {
				addr = "native"
			}
			if withCSV {
				mismatch := ""
				if r.Mismatch {
					mismatch = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, id, r.Symbol, r.Balance, orDash(r.CSVAmount), mismatch, addr)
				return
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, id, r.Symbol, r.Balance, addr)
		}
		for _, chain := range chains {
			line(native[chain])
			for _, token := range tokens {
				if token.Chain == chain {
					line(token)
				}
			}
		}
		tw.Flush()
		if len(failed) > 0 {
			fmt.Fprintln(out, "\nSkipped:")
			tw = newTable(out)
			for _, f := range failed {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", strings.TrimPrefix(f.Chain, "eip155:"), f.Code, f.Message)
			}
			tw.Flush()
		}
		if hint != "" {
			fmt.Fprintln(out, "\n"+hint)
		}
	}}, nil
}

// chainName is the chain-config short name (for example arbitrum), or the
// numeric ID when the chain is unknown.
func chainName(chain uint64) string {
	if n, err := networks.NetworkByNumber(chain); err == nil && n != nil && n.InternalName != "" {
		return n.InternalName
	}
	return strconv.FormatUint(chain, 10)
}
