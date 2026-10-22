package cli

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

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
		address := strings.TrimSpace(row[1])
		if !onchain.ValidAddress(address) {
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
	type row struct {
		Chain     string `json:"chain"`
		Address   string `json:"address"`
		Symbol    string `json:"symbol"`
		Decimals  int    `json:"decimals"`
		Balance   string `json:"balance"`
		Raw       string `json:"raw"`
		CSVAmount string `json:"csv_amount,omitempty"`
		Mismatch  bool   `json:"mismatch"`
	}
	tokens := []row{}
	native := map[string]row{}
	chains := []string{}
	ctx, cancel := e.requestCtx()
	defer cancel()
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	for _, id := range ids {
		chain := fmt.Sprintf("eip155:%d", id)
		chains = append(chains, chain)
		balance, er := rpc.Balance(ctx, id, w.agent)
		if er != nil {
			return nil, output.Wrap(output.CodeUpstream, er, "native balance")
		}
		native[chain] = row{Chain: chain, Symbol: "ETH", Decimals: 18, Balance: formatUnits(balance, 18), Raw: balance.String()}
		candidates := manifest[id]
		addresses := make([]string, len(candidates))
		for i, c := range candidates {
			addresses[i] = c.Address
		}
		details, er := rpc.TokenDetails(ctx, id, w.agent, addresses)
		if er != nil {
			return nil, output.Wrap(output.CodeUpstream, er, "multicall token metadata")
		}
		for _, d := range details {
			csvAmount := ""
			for _, c := range candidates {
				if strings.EqualFold(c.Address, d.Address) {
					csvAmount = c.Amount
					break
				}
			}
			actual := formatUnits(d.Balance, d.Decimals)
			tokens = append(tokens, row{Chain: chain, Address: d.Address, Symbol: d.Symbol, Decimals: d.Decimals, Balance: actual, Raw: d.Balance.String(), CSVAmount: csvAmount, Mismatch: csvAmount != "" && csvAmount != actual})
		}
	}
	hint := ""
	if e.cmd.String("tokens-file") == "" {
		hint = "Provide --tokens-file FILE to check ERC-20 holdings against a CSV manifest."
	}
	data := map[string]any{"agent": w.agent, "chains": chains, "native": native, "tokens": tokens}
	if hint != "" {
		data["hint"] = hint
	}
	return &result{data: data, human: func(out io.Writer) {
		fmt.Fprintf(out, "Agent %s\n", w.agent)
		for _, chain := range chains {
			fmt.Fprintf(out, "%s native: %s (%s raw)\n", chain, native[chain].Balance, native[chain].Raw)
		}
		for _, token := range tokens {
			fmt.Fprintf(out, "%s %s (%s): %s (%s raw), CSV %s, mismatch %t\n", token.Chain, token.Symbol, token.Address, token.Balance, token.Raw, token.CSVAmount, token.Mismatch)
		}
		if hint != "" {
			fmt.Fprintln(out, hint)
		}
	}}, nil
}
