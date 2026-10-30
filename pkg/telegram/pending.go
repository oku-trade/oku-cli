package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	// pendingTTL bounds how long a summary can be confirmed.
	pendingTTL = 2 * time.Minute
	// inputTTL bounds how long the bot waits for a one-line input.
	inputTTL = 2 * time.Minute
	// maxCallbackData is the Bot API limit for callback_data.
	maxCallbackData = 64
)

// submitCall is one backend call Confirm runs.
type submitCall struct {
	path  string
	flags map[string]any
}

// pendingAction is a reviewed action waiting for Confirm. It lives only in
// memory on the bot host. Confirm runs exactly the stored calls: nothing a
// user types after the summary is used.
type pendingAction struct {
	id        string
	userID    int64
	chatID    int64
	messageID int64
	kind      string
	// intentID and digest come from the prepare response (onchain kinds).
	intentID  string
	digest    string
	calls     []submitCall
	createdAt time.Time
	expiresAt time.Time
}

// Kinds of pending action.
const (
	kindSwap       = "swap"
	kindSend       = "send"
	kindBridge     = "bridge"
	kindExit       = "exit"
	kindExitCancel = "exit-cancel"
	kindPerpCancel = "perps-cancel"
	kindClaim      = "claim"
)

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// newPendingID returns a 128-bit random ID.
func newPendingID() string { return randomHex(16) }

// putPending stores p as the user's only pending action, replacing any
// earlier one.
func (b *bot) putPending(p *pendingAction) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.await, p.userID)
	b.pending[p.userID] = p
}

func (b *bot) dropPending(user int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, user)
}

// expiryFor returns min(now+pendingTTL, quote expiry).
func (b *bot) expiryFor(quoteExpiry string) time.Time {
	exp := b.now().Add(pendingTTL)
	if quoteExpiry != "" {
		if t, err := time.Parse(time.RFC3339Nano, quoteExpiry); err == nil && t.Before(exp) {
			exp = t
		}
	}
	return exp
}

// take atomically consumes the pending action id if it belongs to user,
// chat and msg and has not expired. Exactly one caller can win for an ID.
func (b *bot) take(id string, user, chat, msg int64) (*pendingAction, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pending[user]
	if p == nil || p.id != id {
		return nil, "This action expired or was already used. Nothing was sent."
	}
	if p.userID != user || p.chatID != chat || p.messageID != msg {
		return nil, "This action expired or was already used. Nothing was sent."
	}
	if !b.now().Before(p.expiresAt) {
		delete(b.pending, user)
		return nil, "This action expired. Nothing was sent. Start again from the menu."
	}
	delete(b.pending, user)
	return p, ""
}

func confirmRow(id string) []Button {
	return row(btn("Confirm", "c:"+id), btn("Cancel", "x:"+id))
}

func (b *bot) confirm(ctx context.Context, user, chat, msg int64, id string) {
	p, reason := b.take(id, user, chat, msg)
	if p == nil {
		b.edit(ctx, chat, msg, view{text: reason, kb: mainOnly()})
		return
	}
	b.edit(ctx, chat, msg, view{text: "Working... This can take a few minutes for onchain actions. Other requests wait until it finishes."})
	var lines []string
	for _, c := range p.calls {
		timeout := submitTimeout
		exitOrder := c.path == "swap orders create" || c.path == "swap orders cancel"
		if exitOrder {
			timeout = readTimeout
		}
		res, err := b.call(ctx, timeout, c.path, c.flags)
		if err != nil {
			// An exit order call that timed out may still have been
			// stored by the backend, so it must not be retried blindly.
			if isOutcomeUnknown(err) || (exitOrder && isUpstreamTimeout(err)) {
				b.edit(ctx, chat, msg, view{text: outcomeUnknownText(p, c), kb: mainOnly()})
				return
			}
			if len(p.calls) > 1 {
				lines = append(lines, fmt.Sprintf("%s: %s", c.flags["intent"], userError(err)))
				continue
			}
			b.edit(ctx, chat, msg, view{text: userError(err), kb: mainOnly()})
			return
		}
		lines = append(lines, renderSubmit(p, c, res))
	}
	b.edit(ctx, chat, msg, view{text: strings.Join(lines, "\n\n"), kb: mainOnly()})
}

func outcomeUnknownText(p *pendingAction, c submitCall) string {
	var status string
	switch p.kind {
	case kindSwap, kindSend, kindBridge:
		status = fmt.Sprintf("oku %s status --intent %s", p.kind, p.intentID)
	case kindPerpCancel:
		status = fmt.Sprintf("oku orders status --intent %s", c.flags["intent-id"])
	case kindClaim:
		status = fmt.Sprintf("oku bridge status --intent %s", c.flags["intent"])
	default:
		status = "oku swap orders list"
	}
	return "Outcome unknown. Do not retry.\nRun `" + status + "` on the bot host."
}

func (b *bot) cancel(ctx context.Context, user, chat, msg int64, id string) {
	b.mu.Lock()
	p := b.pending[user]
	ok := p != nil && p.id == id && p.chatID == chat && p.messageID == msg
	if ok {
		delete(b.pending, user)
	}
	b.mu.Unlock()
	if !ok {
		b.edit(ctx, chat, msg, view{text: "This action expired or was already used. Nothing was sent.", kb: mainOnly()})
		return
	}
	text := "Cancelled. Nothing was sent."
	switch p.kind {
	case kindSwap, kindSend, kindBridge:
		text += "\nThe prepared " + p.kind + " intent " + p.intentID + " stays in the local journal as prepared and expires on its own."
	}
	b.edit(ctx, chat, msg, view{text: text, kb: mainOnly()})
}

// prepareFlow runs the prepare step for a parsed one-line input and shows
// the summary with Confirm and Cancel.
func (b *bot) prepareFlow(ctx context.Context, user, chat int64, kind string, flags map[string]any) {
	b.dropPending(user)
	if kind == kindExit {
		b.exitCreateSummary(ctx, user, chat, flags)
		return
	}
	msg := b.send(ctx, chat, view{text: "Preparing " + kind + "..."})
	if msg == 0 {
		return
	}
	res, err := b.call(ctx, prepareTimeout, kind+" prepare", flags)
	if err != nil {
		b.edit(ctx, chat, msg, view{text: userError(err), kb: kb(row(btn("Back", "m:"+kind)), row(btn("Menu", "m:main")))})
		return
	}
	rec := asMap(res)
	intentID, digest := str(rec, "intent_id"), str(rec, "digest")
	if intentID == "" || digest == "" || str(rec, "state") != "prepared" {
		b.edit(ctx, chat, msg, view{text: genericError, kb: mainOnly()})
		return
	}
	summary := prepareSummary(kind, flags, rec)
	val := asMap(rec["valuation"])
	if v, _ := val["high_impact"].(bool); v {
		b.edit(ctx, chat, msg, view{text: summary + "\n\nHigh price impact. The bot will not submit this. Review it with the CLI.", kb: mainOnly()})
		return
	}
	if kind == kindSend && isMax(fmt.Sprint(flags["amount"])) && str(rec, "resolved_amount") == "" {
		b.edit(ctx, chat, msg, view{text: summary + "\n\nThe resolved amount is unknown, so the bot will not send max. Use an exact amount.", kb: mainOnly()})
		return
	}
	p := &pendingAction{
		id: newPendingID(), userID: user, chatID: chat, messageID: msg, kind: kind,
		intentID: intentID, digest: digest,
		calls:     []submitCall{{path: kind + " submit", flags: map[string]any{"intent": intentID, "digest": digest}}},
		createdAt: b.now(), expiresAt: b.expiryFor(str(rec, "expires_at")),
	}
	b.putPending(p)
	b.edit(ctx, chat, msg, view{text: summary + "\n\nConfirm within 2 minutes.", kb: kb(confirmRow(p.id))})
}

func isMax(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "max" || s == "all"
}

func prepareSummary(kind string, flags map[string]any, rec map[string]any) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Review %s\n", kind)
	switch kind {
	case kindSend:
		fmt.Fprintf(&sb, "Chain: %s\nToken: %s\nAmount: %s\n", str(rec, "chain_id"), str(rec, "token"), str(rec, "amount"))
		if r := str(rec, "resolved_amount"); r != "" {
			fmt.Fprintf(&sb, "Resolved amount (max): %s\n", r)
		}
		fmt.Fprintf(&sb, "Recipient: %s\n", str(rec, "recipient"))
	default:
		chain := str(rec, "chain_id")
		if oc := str(rec, "out_chain"); oc != "" && oc != "0" {
			chain += " -> " + oc
		}
		fmt.Fprintf(&sb, "Chain: %s\nFrom: %v\nTo: %v\nAmount: %v\n", chain, flags["from"], flags["to"], flags["amount"])
		if r := str(rec, "resolved_amount"); r != "" && isMax(fmt.Sprint(flags["amount"])) {
			fmt.Fprintf(&sb, "Resolved amount (max): %s\n", r)
		}
		fmt.Fprintf(&sb, "Recipient: %s\nRoute: %s\nMinimum out (raw): %s\n", str(rec, "recipient"), orDash(str(rec, "route")), orDash(str(rec, "min_out")))
		val := asMap(rec["valuation"])
		if len(val) > 0 {
			fmt.Fprintf(&sb, "Value: $%s in, $%s out, loss %s bps\n", usd(val["in_usd"]), usd(val["out_usd"]), str(val, "loss_bps"))
		}
		if e := str(rec, "expires_at"); e != "" {
			fmt.Fprintf(&sb, "Quote expires: %s\n", e)
		}
	}
	fmt.Fprintf(&sb, "Intent: %s\nDigest: %s", str(rec, "intent_id"), shortDigest(str(rec, "digest")))
	return sb.String()
}

func shortDigest(d string) string {
	if len(d) > 16 {
		return d[:8] + "..." + d[len(d)-8:]
	}
	return d
}

func usd(v any) string {
	f, ok := v.(float64)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.2f", f)
}

func renderSubmit(p *pendingAction, c submitCall, res any) string {
	m := asMap(res)
	var sb strings.Builder
	switch p.kind {
	case kindSwap, kindSend, kindBridge:
		fmt.Fprintf(&sb, "%s %s: %s", p.kind, p.intentID, orDash(str(m, "state")))
		writeHashes(&sb, m)
	case kindExit:
		fmt.Fprintf(&sb, "Exit order %s: %s\nIt fires only while `oku swap orders watch` runs on a host.", str(m, "order_id"), orDash(str(m, "state")))
	case kindExitCancel:
		fmt.Fprintf(&sb, "Exit order %s: %s", c.flags["order"], orDash(str(m, "state")))
	case kindPerpCancel:
		fmt.Fprintf(&sb, "Cancel %v %v: %s", c.flags["market"], c.flags["order-id"], orDash(str(m, "state")))
		if s := str(m, "server_state"); s != "" {
			fmt.Fprintf(&sb, " (server: %s)", s)
		}
	case kindClaim:
		fmt.Fprintf(&sb, "Claim %v: %s", c.flags["intent"], orDash(str(m, "status")))
		writeHashes(&sb, asMap(m["claim"]))
	}
	return sb.String()
}

func writeHashes(sb *strings.Builder, m map[string]any) {
	for _, h := range list(m["tx_hashes"]) {
		fmt.Fprintf(sb, "\nTx: %v", h)
	}
	if u := str(m, "explorer_url"); u != "" {
		fmt.Fprintf(sb, "\nExplorer: %s", u)
	}
}

// exitCreateSummary shows the typed exit order parameters. swap orders
// create has no prepare step, so this summary is the review.
func (b *bot) exitCreateSummary(ctx context.Context, user, chat int64, flags map[string]any) {
	flags["id"] = "so-tg-" + randomHex(8)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Review exit order\nChain: %v\nToken: %v\n", flags["chain"], flags["token"])
	if flags["all"] == true {
		sb.WriteString("Amount: full balance when triggered\n")
	} else {
		fmt.Fprintf(&sb, "Amount: %v\n", flags["amount"])
	}
	for _, k := range []struct{ flag, label string }{{"take-profit-pct", "Take profit: +%v%%\n"}, {"stop-loss-pct", "Stop loss: -%v%%\n"}, {"trailing-stop-pct", "Trailing stop: -%v%% from peak\n"}} {
		if v, ok := flags[k.flag]; ok {
			fmt.Fprintf(&sb, k.label, v)
		}
	}
	fmt.Fprintf(&sb, "Order ID: %v\n\nExits fire only while `oku swap orders watch` runs on a host. Confirm within 2 minutes.", flags["id"])
	msg := b.send(ctx, chat, view{text: "Preparing exit order..."})
	if msg == 0 {
		return
	}
	p := &pendingAction{id: newPendingID(), userID: user, chatID: chat, messageID: msg, kind: kindExit,
		calls: []submitCall{{path: "swap orders create", flags: flags}}, createdAt: b.now(), expiresAt: b.expiryFor("")}
	b.putPending(p)
	b.edit(ctx, chat, msg, view{text: sb.String(), kb: kb(confirmRow(p.id))})
}

// startPerpsCancel builds the review for cancelling one open perps order
// from fresh orders list data, bound to its (market, oid).
func (b *bot) startPerpsCancel(ctx context.Context, user, chat, msg int64, oidText string) {
	b.dropPending(user)
	res, err := b.call(ctx, readTimeout, "orders list", map[string]any{"fresh": true})
	if err != nil {
		b.edit(ctx, chat, msg, view{text: userError(err), kb: backRefresh("orders", "perps")})
		return
	}
	var order map[string]any
	for _, o := range list(asMap(res)["orders"]) {
		om := asMap(o)
		if str(om, "oid") == oidText {
			order = om
		}
	}
	if order == nil {
		b.edit(ctx, chat, msg, view{text: "That order is no longer open.", kb: backRefresh("orders", "perps")})
		return
	}
	var oid int64
	if _, err := fmt.Sscan(oidText, &oid); err != nil || oid <= 0 {
		b.edit(ctx, chat, msg, view{text: genericError, kb: mainOnly()})
		return
	}
	market := str(order, "coin")
	intent := "tg-cancel-" + randomHex(8)
	p := &pendingAction{id: newPendingID(), userID: user, chatID: chat, messageID: msg, kind: kindPerpCancel,
		calls:     []submitCall{{path: "orders cancel", flags: map[string]any{"market": market, "order-id": oid, "intent-id": intent}}},
		createdAt: b.now(), expiresAt: b.expiryFor("")}
	b.putPending(p)
	text := fmt.Sprintf("Review perps cancel\nMarket: %s\nSide: %s\nSize: %s\nPrice: %s\nOID: %d\n\nCancelling submits immediately on Confirm. Confirm within 2 minutes.",
		market, sideName(str(order, "side")), str(order, "sz"), str(order, "limitPx"), oid)
	b.edit(ctx, chat, msg, view{text: text, kb: kb(confirmRow(p.id))})
}

// startSwapOrderCancel builds the review for cancelling one active exit order.
func (b *bot) startSwapOrderCancel(ctx context.Context, user, chat, msg int64, orderID string) {
	b.dropPending(user)
	res, err := b.call(ctx, readTimeout, "swap orders list", map[string]any{"state": "active"})
	if err != nil {
		b.edit(ctx, chat, msg, view{text: userError(err), kb: backRefresh("exits", "main")})
		return
	}
	var order map[string]any
	for _, o := range list(asMap(res)["orders"]) {
		om := asMap(o)
		if str(om, "order_id") == orderID && str(om, "state") == "active" {
			order = om
		}
	}
	if order == nil {
		b.edit(ctx, chat, msg, view{text: "That exit order is no longer active.", kb: backRefresh("exits", "main")})
		return
	}
	p := &pendingAction{id: newPendingID(), userID: user, chatID: chat, messageID: msg, kind: kindExitCancel,
		calls:     []submitCall{{path: "swap orders cancel", flags: map[string]any{"order": orderID}}},
		createdAt: b.now(), expiresAt: b.expiryFor("")}
	b.putPending(p)
	text := fmt.Sprintf("Review exit order cancel\nOrder: %s\nChain: %s\nToken: %s\nAmount: %s\nState: %s\n\nConfirm within 2 minutes.",
		orderID, str(order, "chain_id"), str(order, "token"), orDash(str(order, "amount")), str(order, "state"))
	b.edit(ctx, chat, msg, view{text: text, kb: kb(confirmRow(p.id))})
}

// startClaim lists the bridges that need a claim now. Confirm claims
// exactly these intents, one by one.
func (b *bot) startClaim(ctx context.Context, user, chat, msg int64) {
	b.dropPending(user)
	res, err := b.call(ctx, slowTimeout, "bridge status", map[string]any{"all": true})
	if err != nil {
		b.edit(ctx, chat, msg, view{text: userError(err), kb: backRefresh("bridge", "main")})
		return
	}
	var sb strings.Builder
	sb.WriteString("Review bridge claims\n")
	var calls []submitCall
	for _, e := range list(res) {
		m := asMap(e)
		if claim, _ := m["claim_required"].(bool); !claim || str(m, "state") == "prepared" {
			continue
		}
		id := str(m, "intent_id")
		calls = append(calls, submitCall{path: "bridge claim", flags: map[string]any{"intent": id}})
		fmt.Fprintf(&sb, "%s: %s -> %s, %s via %s\n", id, str(m, "source_chain"), str(m, "destination_chain"), str(m, "amount"), orDash(str(m, "route")))
	}
	if len(calls) == 0 {
		b.edit(ctx, chat, msg, view{text: "No bridges are ready to claim.", kb: backRefresh("bridge", "main")})
		return
	}
	p := &pendingAction{id: newPendingID(), userID: user, chatID: chat, messageID: msg, kind: kindClaim,
		calls: calls, createdAt: b.now(), expiresAt: b.expiryFor("")}
	b.putPending(p)
	sb.WriteString("\nConfirm within 2 minutes.")
	b.edit(ctx, chat, msg, view{text: sb.String(), kb: kb(confirmRow(p.id))})
}
