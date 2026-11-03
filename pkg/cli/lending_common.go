package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/lending"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

// Lending protocols the earn, borrow and loop commands support.
const (
	protoMorpho = "morpho"
	protoEuler  = "euler"
)

var lendingProtocols = []string{protoMorpho, protoEuler}

// lendingTarget is the parsed --chain/--protocol pair every lending command
// starts from.
type lendingTarget struct {
	chain    uint64
	protocol string
}

func (t lendingTarget) caip2() string { return fmt.Sprintf("eip155:%d", t.chain) }

func parseLendingTarget(e *env) (lendingTarget, error) {
	chain, err := strconv.ParseUint(strings.TrimPrefix(str(e, "chain"), "eip155:"), 10, 64)
	if err != nil || !onchain.Allowed(chain) {
		return lendingTarget{}, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
	}
	p := strings.ToLower(str(e, "protocol"))
	if p == "" {
		p = protoMorpho
	}
	t := lendingTarget{chain: chain, protocol: p}
	switch p {
	case protoMorpho:
		if _, err := lending.Morpho(chain); err != nil {
			return t, output.Wrap(output.CodeInvalidArgument, err, "morpho")
		}
	case protoEuler:
		if _, err := lending.Euler(chain); err != nil {
			return t, output.Wrap(output.CodeInvalidArgument, err, "euler")
		}
	default:
		return t, output.Errorf(output.CodeInvalidArgument, "--protocol must be morpho or euler")
	}
	return t, nil
}

func lendingTargetFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID, e.g. 8453 (needs OKU_RPC_URL_<id>)"},
		&urfave.StringFlag{Name: "protocol", Value: protoMorpho, Usage: "morpho or euler"},
	}
}

// The Morpho indexer endpoint is the one the Oku web app is configured
// with. It is discovered from the web app's public runtime config (the same
// origin `wallet fund` links to), unless config overrides it.
var sapURLPattern = regexp.MustCompile(`SapRpc\s*:\s*\{[^}]*?url\s*:\s*["']([^"']+)["']`)

func (a *App) morphoIndexer(ctx context.Context) (lending.SAP, error) {
	ua := "oku-cli/" + a.Version
	cfg, err := config.LoadLendingEndpoints(a.Getenv)
	if err != nil {
		return lending.SAP{}, output.Wrap(output.CodeConfigError, err, "lending endpoints")
	}
	if cfg.MorphoIndexer != nil {
		return lending.SAP{URL: cfg.MorphoIndexer.String(), UserAgent: ua}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.WebOrigin.JoinPath("config.js").String(), nil)
	if err != nil {
		return lending.SAP{}, err
	}
	req.Header.Set("User-Agent", ua)
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return lending.SAP{}, output.Wrap(output.CodeUpstream, err, "reading the Oku web config for the Morpho indexer; set OKU_MORPHO_API_URL")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return lending.SAP{}, output.Errorf(output.CodeUpstream, "Oku web config unavailable (HTTP %d); set OKU_MORPHO_API_URL", resp.StatusCode)
	}
	m := sapURLPattern.FindSubmatch(body)
	if m == nil {
		return lending.SAP{}, output.Errorf(output.CodeUpstream, "Oku web config has no Morpho indexer; set OKU_MORPHO_API_URL")
	}
	if _, err = config.ParseHTTPSURL(string(m[1])); err != nil {
		return lending.SAP{}, output.Wrap(output.CodeUpstream, err, "Morpho indexer URL in the Oku web config")
	}
	return lending.SAP{URL: string(m[1]), UserAgent: ua}, nil
}

// lendingAmount parses a positive decimal amount in token units.
func lendingAmount(text string, decimals int, what string) (*big.Int, error) {
	v, err := parseUnits(text, decimals)
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidDecimal, err, what)
	}
	return v, nil
}

// optFloat is a number that may be unknown. Unknown renders as null in JSON
// and N/A for humans, never as zero.
type optFloat struct {
	v  float64
	ok bool
}

func some(v float64) optFloat { return optFloat{v, true} }

func (o optFloat) MarshalJSON() ([]byte, error) {
	if !o.ok {
		return []byte("null"), nil
	}
	return []byte(strconv.FormatFloat(o.v, 'f', -1, 64)), nil
}

func (o optFloat) pct() string {
	if !o.ok {
		return "N/A"
	}
	return fmt.Sprintf("%.2f%%", o.v*100)
}

func (o optFloat) num(prec int) string {
	if !o.ok {
		return "N/A"
	}
	return strconv.FormatFloat(o.v, 'f', prec, 64)
}

func optPtr(p *float64) optFloat {
	if p == nil {
		return optFloat{}
	}
	return some(*p)
}

func parseOpt(s string) optFloat {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return optFloat{}
	}
	return some(v)
}

// eulerSubAccounts is every EVC sub-account index (0-255) the CLI accepts,
// so discovery never misses a position prepare can act on.
const eulerSubAccounts = 256

// validDecimals bounds token decimals from upstream metadata before they
// size amounts.
func validDecimals(d int) bool { return d >= 0 && d <= 36 }

func addr(s string) common.Address { return common.HexToAddress(s) }
