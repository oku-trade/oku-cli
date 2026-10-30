package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ordersPerPage bounds the perps orders shown per message (one Cancel
// button each), so long lists are paged instead of truncated.
const ordersPerPage = 10

func btn(text, data string) Button  { return Button{Text: text, CallbackData: data} }
func row(b ...Button) []Button      { return b }
func kb(rows ...[]Button) *Keyboard { return &Keyboard{InlineKeyboard: rows} }
func mainOnly() *Keyboard           { return kb(row(btn("Menu", "m:main"))) }

// backRefresh is the standard sub-screen footer.
func backRefresh(screen, back string) *Keyboard {
	return kb(row(btn("Refresh", "m:"+screen), btn("Back", "m:"+back)))
}

func withFooter(rows [][]Button, screen, back string) *Keyboard {
	return kb(append(rows, row(btn("Refresh", "m:"+screen), btn("Back", "m:"+back)))...)
}

func mainKeyboard() *Keyboard {
	return kb(
		row(btn("Wallet", "m:wallet"), btn("PnL", "m:pnl")),
		row(btn("Perps", "m:perps"), btn("Swap", "m:swap")),
		row(btn("Send", "m:send"), btn("Bridge", "m:bridge")),
		row(btn("Exit orders", "m:exits"), btn("Status", "m:status")),
	)
}

// screen renders a read-only screen. Swap, Send, Bridge and exit creation
// arm the one-line input for the user.
func (b *bot) screen(ctx context.Context, user int64, name string) view {
	page := 0
	if base, p, ok := strings.Cut(name, ":"); ok {
		name = base
		page, _ = strconv.Atoi(p)
	}
	switch name {
	case "wallet":
		return b.walletScreen(ctx, false)
	case "wallet_all":
		return b.walletScreen(ctx, true)
	case "fund":
		return b.fundScreen(ctx)
	case "pnl":
		return b.pnlScreen(ctx)
	case "pnl_all":
		if !b.be.Has("pnl") {
			return view{text: "PnL is not available in this build.", kb: backRefresh("pnl", "main")}
		}
		return b.readScreen(ctx, "pnl", nil, "pnl_all", "pnl", renderGeneric("PnL"))
	case "perps":
		v := b.readScreen(ctx, "account summary", nil, "perps", "main", renderAccount)
		v.kb.InlineKeyboard = append([][]Button{row(btn("Positions", "m:positions"), btn("Orders", "m:orders"), btn("Fills", "m:fills"))}, v.kb.InlineKeyboard...)
		return v
	case "positions":
		return b.readScreen(ctx, "positions list", nil, "positions", "perps", renderPositions)
	case "orders":
		return b.ordersScreen(ctx, page)
	case "fills":
		return b.readScreen(ctx, "fills list", nil, "fills", "perps", renderFills)
	case kindSwap, kindSend:
		b.setAwait(user, name)
		return view{text: inputUsage[name], kb: kb(row(btn("Back", "m:main")))}
	case kindBridge:
		return b.bridgeScreen(ctx, user)
	case "exits":
		return b.exitsScreen(ctx)
	case "exit_new":
		b.setAwait(user, kindExit)
		return view{text: inputUsage[kindExit], kb: kb(row(btn("Back", "m:exits")))}
	case "status":
		return b.statusScreen(ctx)
	}
	b.clearAwait(user)
	header := "Oku Agent"
	if b.profile != "" {
		header += " (profile " + b.profile + ")"
	}
	return view{text: header + "\nChoose an action.", kb: mainKeyboard()}
}

// readScreen runs one read and renders it with the standard footer.
func (b *bot) readScreen(ctx context.Context, path string, opts map[string]any, screen, back string, render func(any) string) view {
	res, err := b.call(ctx, readTimeout, path, opts)
	if err != nil {
		return view{text: userError(err), kb: backRefresh(screen, back)}
	}
	return view{text: render(res), kb: backRefresh(screen, back)}
}

func (b *bot) walletScreen(ctx context.Context, all bool) view {
	var sb strings.Builder
	addr, err := b.call(ctx, readTimeout, "wallet address", nil)
	if err != nil {
		return view{text: userError(err), kb: backRefresh("wallet", "main")}
	}
	am := asMap(addr)
	fmt.Fprintf(&sb, "Agent wallet: %s\nMaster: %s\n\n", str(am, "agent"), str(am, "master"))
	opts, timeout, screen := map[string]any{"chain": "42161"}, readTimeout, "wallet"
	if all {
		opts, timeout, screen = map[string]any{"all-chains": true}, slowTimeout, "wallet_all"
	}
	bal, err := b.call(ctx, timeout, "wallet balances", opts)
	if err != nil {
		sb.WriteString("Balances: " + userError(err))
	} else {
		sb.WriteString(renderBalances(bal))
	}
	rows := [][]Button{row(btn("All chains", "m:wallet_all"), btn("Fund", "m:fund"))}
	if all {
		rows = [][]Button{row(btn("Arbitrum only", "m:wallet"), btn("Fund", "m:fund"))}
	}
	return view{text: sb.String(), kb: withFooter(rows, screen, "main")}
}

func (b *bot) fundScreen(ctx context.Context) view {
	res, err := b.call(ctx, readTimeout, "wallet fund", map[string]any{"no-open": true})
	if err != nil {
		return view{text: userError(err), kb: backRefresh("fund", "wallet")}
	}
	m := asMap(res)
	text := fmt.Sprintf("Fund agent %s on chain %s (%s)\nWeb: %s\nPayment URI: %s\n\nVerify the chain, token and address in your paying wallet.",
		str(m, "agent"), str(m, "chain"), str(m, "token"), str(m, "web_url"), str(m, "payment_uri"))
	return view{text: text, kb: backRefresh("fund", "wallet")}
}

func (b *bot) pnlScreen(ctx context.Context) view {
	v := b.readScreen(ctx, "swap pnl", nil, "pnl", "main", renderPnL)
	if b.be.Has("pnl") {
		v.kb.InlineKeyboard = append([][]Button{row(btn("PnL", "m:pnl_all"))}, v.kb.InlineKeyboard...)
	}
	return v
}

func (b *bot) ordersScreen(ctx context.Context, page int) view {
	res, err := b.call(ctx, readTimeout, "orders list", map[string]any{"fresh": true})
	if err != nil {
		return view{text: userError(err), kb: backRefresh("orders", "perps")}
	}
	orders := list(asMap(res)["orders"])
	if len(orders) == 0 {
		return view{text: "No open orders.", kb: backRefresh("orders", "perps")}
	}
	pages := (len(orders) + ordersPerPage - 1) / ordersPerPage
	page = max(0, min(page, pages-1))
	var sb strings.Builder
	fmt.Fprintf(&sb, "Open orders (%d, page %d of %d)\n", len(orders), page+1, pages)
	var rows [][]Button
	for _, o := range orders[page*ordersPerPage : min(len(orders), (page+1)*ordersPerPage)] {
		m := asMap(o)
		oid := str(m, "oid")
		fmt.Fprintf(&sb, "\n%s %s %s @ %s (oid %s)", str(m, "coin"), sideName(str(m, "side")), str(m, "sz"), str(m, "limitPx"), oid)
		if okOID(oid) {
			rows = append(rows, row(btn(fmt.Sprintf("Cancel %s %s", str(m, "coin"), oid), "po:"+oid)))
		}
	}
	var nav []Button
	if page > 0 {
		nav = append(nav, btn("Prev", fmt.Sprintf("m:orders:%d", page-1)))
	}
	if page < pages-1 {
		nav = append(nav, btn("Next", fmt.Sprintf("m:orders:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	return view{text: sb.String(), kb: withFooter(rows, "orders", "perps")}
}

var oidRE = regexp.MustCompile(`^[0-9]{1,19}$`)

func okOID(s string) bool { return oidRE.MatchString(s) }

func (b *bot) bridgeScreen(ctx context.Context, user int64) view {
	b.setAwait(user, kindBridge)
	var sb strings.Builder
	sb.WriteString(inputUsage[kindBridge])
	res, err := b.call(ctx, slowTimeout, "bridge status", map[string]any{"all": true})
	rows := [][]Button{}
	if err != nil {
		sb.WriteString("\n\nUnsettled bridges: " + userError(err))
	} else {
		text, claimable := renderBridges(res)
		sb.WriteString("\n\n" + text)
		if claimable {
			rows = append(rows, row(btn("Claim ready", "cl")))
		}
	}
	return view{text: sb.String(), kb: withFooter(rows, "bridge", "main")}
}

func (b *bot) exitsScreen(ctx context.Context) view {
	res, err := b.call(ctx, readTimeout, "swap orders list", nil)
	rows := [][]Button{row(btn("Create exit", "m:exit_new"))}
	if err != nil {
		return view{text: userError(err), kb: withFooter(rows, "exits", "main")}
	}
	text, active := renderExitOrders(res)
	for _, id := range active {
		rows = append(rows, row(btn("Cancel "+id, "so:"+id)))
	}
	return view{text: text + "\n\nExits fire only while `oku swap orders watch` runs on a host.", kb: withFooter(rows, "exits", "main")}
}

func (b *bot) statusScreen(ctx context.Context) view {
	var sb strings.Builder
	res, err := b.call(ctx, slowTimeout, "bridge status", map[string]any{"all": true})
	if err != nil {
		sb.WriteString("Bridges: " + userError(err))
	} else {
		text, _ := renderBridges(res)
		sb.WriteString(text)
	}
	so, err := b.call(ctx, readTimeout, "swap orders list", nil)
	if err != nil {
		sb.WriteString("\n\nExit orders: " + userError(err))
	} else {
		text, _ := renderExitOrders(so)
		sb.WriteString("\n\n" + text)
	}
	return view{text: sb.String(), kb: backRefresh("status", "main")}
}

// One-line inputs. Each field has a strict shape, and the parsed values
// map 1:1 to prepare flags. Nothing else is accepted.
var inputUsage = map[string]string{
	kindSwap:   "Swap: send one line\nchain from to amount [slippage_bps]\nExample: 42161 native usdc 0.01 50\namount may be max.",
	kindSend:   "Send: send one line\nchain token amount [recipient]\nExample: 42161 usdc 5 0xabc...\nThe recipient defaults to your master wallet.",
	kindBridge: "Bridge: send one line\nchain destination_chain from to amount [slippage_bps]\nExample: 42161 8453 usdc usdc 10",
	kindExit:   "Exit order: send one line\nchain token amount|all tp=PCT sl=PCT trail=PCT\nExample: 42161 0xabc... all tp=50 sl=20\nAt least one of tp, sl or trail.",
}

var (
	chainRE   = regexp.MustCompile(`^[0-9]{1,10}$`)
	tokenRE   = regexp.MustCompile(`^(0x[0-9a-fA-F]{40}|[A-Za-z][A-Za-z0-9.]{0,15})$`)
	addressRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	amountRE  = regexp.MustCompile(`^([0-9]{1,30}(\.[0-9]{1,36})?|max|all)$`)
	pctRE     = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,6})?$`)
)

func fieldErr(name string) error { return errors.New("invalid " + name) }

func parseInput(kind, text string) (map[string]any, error) {
	if strings.ContainsAny(text, "\r\n") {
		return nil, errors.New("send exactly one line")
	}
	f := strings.Fields(text)
	chain := func(i int, name string) error {
		if !chainRE.MatchString(f[i]) {
			return fieldErr(name)
		}
		return nil
	}
	token := func(i int, name string) (string, error) {
		if !tokenRE.MatchString(f[i]) {
			return "", fieldErr(name)
		}
		return f[i], nil
	}
	amount := func(i int) (string, error) {
		a := strings.ToLower(f[i])
		if !amountRE.MatchString(a) {
			return "", fieldErr("amount")
		}
		return a, nil
	}
	slippage := func(i int) (int64, error) {
		n, err := strconv.ParseInt(f[i], 10, 64)
		if err != nil || n < 0 || n > 300 || !chainRE.MatchString(f[i]) {
			return 0, errors.New("slippage_bps must be 0-300")
		}
		return n, nil
	}
	out := map[string]any{}
	var err error
	switch kind {
	case kindSwap:
		if len(f) < 4 || len(f) > 5 {
			return nil, errors.New("expected 4 or 5 fields")
		}
		if err = chain(0, "chain"); err != nil {
			return nil, err
		}
		out["chain"] = f[0]
		if out["from"], err = token(1, "from token"); err != nil {
			return nil, err
		}
		if out["to"], err = token(2, "to token"); err != nil {
			return nil, err
		}
		if out["amount"], err = amount(3); err != nil {
			return nil, err
		}
		if len(f) == 5 {
			if out["slippage-bps"], err = slippage(4); err != nil {
				return nil, err
			}
		}
	case kindBridge:
		if len(f) < 5 || len(f) > 6 {
			return nil, errors.New("expected 5 or 6 fields")
		}
		if err = chain(0, "chain"); err != nil {
			return nil, err
		}
		if err = chain(1, "destination_chain"); err != nil {
			return nil, err
		}
		out["chain"], out["destination-chain"] = f[0], f[1]
		if out["from"], err = token(2, "from token"); err != nil {
			return nil, err
		}
		if out["to"], err = token(3, "to token"); err != nil {
			return nil, err
		}
		if out["amount"], err = amount(4); err != nil {
			return nil, err
		}
		if len(f) == 6 {
			if out["slippage-bps"], err = slippage(5); err != nil {
				return nil, err
			}
		}
	case kindSend:
		if len(f) < 3 || len(f) > 4 {
			return nil, errors.New("expected 3 or 4 fields")
		}
		if err = chain(0, "chain"); err != nil {
			return nil, err
		}
		out["chain"] = f[0]
		if out["token"], err = token(1, "token"); err != nil {
			return nil, err
		}
		if out["amount"], err = amount(2); err != nil {
			return nil, err
		}
		if len(f) == 4 {
			if !addressRE.MatchString(f[3]) {
				return nil, fieldErr("recipient")
			}
			out["to"] = f[3]
		}
	case kindExit:
		if len(f) < 4 || len(f) > 6 {
			return nil, errors.New("expected chain, token, amount and 1 to 3 of tp=, sl=, trail=")
		}
		if err = chain(0, "chain"); err != nil {
			return nil, err
		}
		out["chain"] = f[0]
		if !addressRE.MatchString(f[1]) {
			return nil, errors.New("token must be an ERC-20 address")
		}
		out["token"] = f[1]
		a, err := amount(2)
		if err != nil {
			return nil, err
		}
		if a == "all" || a == "max" {
			out["all"] = true
		} else {
			out["amount"] = a
		}
		keys := map[string]string{"tp": "take-profit-pct", "sl": "stop-loss-pct", "trail": "trailing-stop-pct"}
		for _, kv := range f[3:] {
			k, v, ok := strings.Cut(strings.ToLower(kv), "=")
			flag, known := keys[k]
			if !ok || !known || !pctRE.MatchString(v) {
				return nil, errors.New("exits must be tp=PCT, sl=PCT or trail=PCT")
			}
			if _, dup := out[flag]; dup {
				return nil, errors.New(k + " given twice")
			}
			out[flag] = v
		}
	default:
		return nil, errors.New("unknown input")
	}
	return out, nil
}

// asMap converts a backend result (often a Go struct) to its JSON object
// form, keeping numbers exact.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	var out map[string]any
	_ = roundTrip(v, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func list(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	var out []any
	_ = roundTrip(v, &out)
	return out
}

func roundTrip(v any, out any) error {
	if v == nil {
		return errors.New("nil")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}

// str renders a scalar JSON value. Objects and arrays render empty.
func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sideName(s string) string {
	switch s {
	case "B":
		return "buy"
	case "A":
		return "sell"
	}
	return s
}

func renderGeneric(title string) func(any) string {
	return func(res any) string {
		raw, err := json.MarshalIndent(res, "", " ")
		if err != nil {
			return title
		}
		return title + "\n" + string(raw)
	}
}

func renderBalances(res any) string {
	m := asMap(res)
	var sb strings.Builder
	sb.WriteString("Balances")
	native := asMap(m["native"])
	keys := make([]string, 0, len(native))
	for k := range native {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := asMap(native[k])
		fmt.Fprintf(&sb, "\n%s: %s %s", strings.TrimPrefix(k, "eip155:"), str(r, "balance"), orDash(str(r, "symbol")))
	}
	for _, t := range list(m["tokens"]) {
		r := asMap(t)
		fmt.Fprintf(&sb, "\n%s: %s %s", strings.TrimPrefix(str(r, "chain"), "eip155:"), str(r, "balance"), orDash(str(r, "symbol")))
	}
	if errs := list(m["errors"]); len(errs) > 0 {
		fmt.Fprintf(&sb, "\n(%d chains could not be read)", len(errs))
	}
	return sb.String()
}

func renderPnL(res any) string {
	m := asMap(res)
	chains := list(m["chains"])
	if len(chains) == 0 {
		return "Swap PnL\nNo journaled swaps."
	}
	var sb strings.Builder
	sb.WriteString("Swap PnL (average cost, net of swap gas)")
	for _, c := range chains {
		cm := asMap(c)
		sym := str(cm, "native_symbol")
		t := asMap(cm["total"])
		fmt.Fprintf(&sb, "\n\nChain %s\nRealized: %s %s", str(cm, "chain_id"), str(t, "realized_native"), sym)
		if u := str(t, "unrealized_native"); u != "" {
			fmt.Fprintf(&sb, "\nUnrealized: %s %s", u, sym)
		}
		if u := str(t, "realized_usd"); u != "" {
			fmt.Fprintf(&sb, " ($%s)", u)
		}
		for _, r := range list(cm["tokens"]) {
			rm := asMap(r)
			name := str(rm, "symbol")
			if name == "" {
				name = str(rm, "token")
			}
			fmt.Fprintf(&sb, "\n- %s: held %s, realized %s, unrealized %s", name, str(rm, "held"), str(rm, "realized_native"), orDash(str(rm, "unrealized_native")))
		}
	}
	return sb.String()
}

func renderAccount(res any) string {
	m := asMap(res)
	ms := asMap(m["margin_summary"])
	bal := asMap(m["balances"])
	return fmt.Sprintf("Perps account\nMode: %s\nAccount value: %s\nMargin used: %s\nWithdrawable: %s\nSpot USDC: %s\nOpen positions: %s",
		str(m, "account_mode"), str(ms, "accountValue"), str(ms, "totalMarginUsed"), str(m, "withdrawable"), orDash(str(bal, "spotUsdcBalance")), str(m, "open_positions"))
}

func renderPositions(res any) string {
	ps := list(asMap(res)["positions"])
	if len(ps) == 0 {
		return "No open positions."
	}
	var sb strings.Builder
	sb.WriteString("Positions")
	for _, p := range ps {
		m := asMap(p)
		fmt.Fprintf(&sb, "\n%s size %s entry %s value %s uPnL %s liq %s", str(m, "coin"), str(m, "szi"), str(m, "entryPx"), str(m, "positionValue"), str(m, "unrealizedPnl"), orDash(str(m, "liquidationPx")))
	}
	return sb.String()
}

func renderFills(res any) string {
	fs := list(asMap(res)["fills"])
	if len(fs) == 0 {
		return "No fills."
	}
	var sb strings.Builder
	sb.WriteString("Recent fills")
	for i, f := range fs {
		if i == 20 {
			fmt.Fprintf(&sb, "\n... and %d more (use the CLI)", len(fs)-20)
			break
		}
		m := asMap(f)
		ts := str(m, "time")
		if ms, err := strconv.ParseInt(ts, 10, 64); err == nil {
			ts = time.UnixMilli(ms).UTC().Format("01-02 15:04")
		}
		fmt.Fprintf(&sb, "\n%s %s %s %s @ %s pnl %s", ts, str(m, "coin"), str(m, "dir"), str(m, "sz"), str(m, "px"), orDash(str(m, "closedPnl")))
	}
	return sb.String()
}

// renderBridges lists unsettled bridges, dropping never-submitted prepared
// intents, and reports whether any needs a claim.
func renderBridges(res any) (string, bool) {
	var sb strings.Builder
	claimable := false
	n := 0
	for _, e := range list(res) {
		m := asMap(e)
		if str(m, "state") == "prepared" {
			continue
		}
		if n == 0 {
			sb.WriteString("Unsettled bridges")
		}
		n++
		claim := ""
		if c, _ := m["claim_required"].(bool); c {
			claim = ", claim required"
			claimable = true
		}
		fmt.Fprintf(&sb, "\n%s: %s -> %s, %s, %s%s (%s)", str(m, "intent_id"), str(m, "source_chain"), str(m, "destination_chain"), str(m, "amount"), str(m, "state"), claim, str(m, "age"))
	}
	if n == 0 {
		return "No unsettled bridges.", false
	}
	return sb.String(), claimable
}

// renderExitOrders lists exit orders and returns the IDs that can be
// cancelled (active only).
func renderExitOrders(res any) (string, []string) {
	orders := list(asMap(res)["orders"])
	if len(orders) == 0 {
		return "No exit orders.", nil
	}
	var sb strings.Builder
	sb.WriteString("Exit orders")
	var active []string
	for _, o := range orders {
		m := asMap(o)
		id := str(m, "order_id")
		amount := str(m, "amount")
		if amount == "" {
			amount = "all"
		}
		fmt.Fprintf(&sb, "\n%s: %s on %s, %s", id, str(m, "token"), str(m, "chain_id"), amount)
		for _, k := range []struct{ key, label string }{{"take_profit_pct", " tp +"}, {"stop_loss_pct", " sl -"}, {"trailing_stop_pct", " trail -"}} {
			if v := str(m, k.key); v != "" {
				sb.WriteString(k.label + v + "%")
			}
		}
		fmt.Fprintf(&sb, " [%s]", str(m, "state"))
		if str(m, "state") == "active" && len("so:"+id) <= maxCallbackData {
			active = append(active, id)
		}
	}
	return sb.String(), active
}
