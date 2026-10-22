package cli

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/mdp/qrterminal/v3"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func walletFundFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "chain", Value: "42161", Usage: "EVM chain ID (default 42161)"},
		&urfave.StringFlag{Name: "token", Value: "native", Usage: "native, usdc, or ERC-20 contract address"},
		&urfave.StringFlag{Name: "amount", Usage: "optional decimal amount"},
		&urfave.BoolFlag{Name: "no-open", Usage: "do not open the Oku funding page"},
	}
}

func (a *App) cmdWalletFund(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	chain, err := strconv.ParseUint(strings.TrimPrefix(e.cmd.String("chain"), "eip155:"), 10, 64)
	if err != nil || !onchain.Allowed(chain) {
		return nil, output.Errorf(output.CodeInvalidArgument, "unsupported EVM chain %q", e.cmd.String("chain"))
	}
	token := strings.ToLower(strings.TrimSpace(e.cmd.String("token")))
	if token == "" || isNativeToken(token) {
		token = "native"
	}
	address := token
	if token == "usdc" {
		ctx, cancel := e.requestCtx()
		defer cancel()
		address, err = a.resolveRouteToken(ctx, e.client(w.profile), chain, "USDC")
		if err != nil {
			return nil, err
		}
	} else if token != "native" && !onchain.ValidAddress(token) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--token must be native, usdc, or an EVM token address")
	}
	amount := strings.TrimSpace(e.cmd.String("amount"))
	payment := fmt.Sprintf("ethereum:%s@%d", w.agent, chain)
	if token != "native" {
		payment = fmt.Sprintf("ethereum:%s@%d/transfer?address=%s", address, chain, w.agent)
	}
	if amount != "" {
		decimals := 18
		if token != "native" {
			ctx, cancel := e.requestCtx()
			defer cancel()
			raw, callErr := (onchain.HTTPRPC{Getenv: a.Getenv}).Call(ctx, chain, address, "0x313ce567")
			if callErr != nil {
				return nil, output.Wrap(output.CodeUpstream, callErr, "reading token decimals")
			}
			if !raw.IsInt64() || raw.Int64() < 0 || raw.Int64() > 36 {
				return nil, output.Errorf(output.CodeInvalidArgument, "unsupported token decimals")
			}
			decimals = int(raw.Int64())
		}
		units, parseErr := parseUnits(amount, decimals)
		if parseErr != nil {
			return nil, output.Wrap(output.CodeInvalidDecimal, parseErr, "amount")
		}
		if token == "native" {
			payment += "?value=" + units.String()
		} else {
			payment += "&uint256=" + units.String()
		}
	}
	base := strings.TrimRight(a.Getenv("OKU_WEB_URL"), "/")
	if base == "" {
		base = "https://oku.trade"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, output.Errorf(output.CodeInvalidArgument, "OKU_WEB_URL must be an HTTPS origin")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/agent/fund"
	query := parsed.Query()
	query.Set("chain", strconv.FormatUint(chain, 10))
	query.Set("token", token)
	if amount != "" {
		query.Set("amount", amount)
	}
	parsed.RawQuery = query.Encode()
	web := parsed.String()
	if !e.cmd.Bool("no-open") && a.StdinIsTTY && a.OpenURL != nil {
		if openErr := a.OpenURL(web); openErr != nil {
			e.diag("Browser did not open: %v", openErr)
		}
	}
	data := map[string]any{"agent": w.agent, "chain": chain, "token": token, "amount": amount, "payment_uri": payment, "web_url": web}
	return &result{data: data, human: func(out io.Writer) {
		fmt.Fprintf(out, "Fund agent %s on chain %d (%s)\nPayment URI: %s\nWeb: %s\n", w.agent, chain, token, payment, web)
		qrterminal.GenerateHalfBlock(payment, qrterminal.L, out)
	}}, nil
}
