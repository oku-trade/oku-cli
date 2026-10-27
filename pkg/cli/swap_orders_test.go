package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
)

const (
	soAgent = "0x1111111111111111111111111111111111111111"
	soToken = "0x912CE59144191C1204E64559FE8253a0e49E6548"
)

// fakeSwapExec records sells and answers prices from a mutable table.
type fakeSwapExec struct {
	price      string
	prepares   int
	submits    int
	prepareErr error
	submitErr  error
	// result is the journal state the sell intent reaches after submit.
	result string
	states map[string]string
}

func (f *fakeSwapExec) exec() swapOrderExec {
	f.states = map[string]string{}
	return swapOrderExec{
		prices: func(chain uint64, tokens []string) (map[string]*big.Rat, error) {
			out := map[string]*big.Rat{}
			for _, t := range tokens {
				out[strings.ToLower(t)] = ratOf(f.price)
			}
			return out, nil
		},
		prepare: func(o *journal.SwapOrder) (string, string, error) {
			f.prepares++
			if f.prepareErr != nil {
				return "", "", f.prepareErr
			}
			id := o.ID + "-sell"
			f.states[id] = "prepared"
			return id, "digest", nil
		},
		submit: func(intent, _ string) error {
			f.submits++
			f.states[intent] = f.result
			return f.submitErr
		},
		intentState: func(intent string) (string, error) {
			s, ok := f.states[intent]
			if !ok {
				return "", journal.ErrNotFound
			}
			return s, nil
		},
	}
}

func openSwapJournal(t *testing.T) *journal.Journal {
	t.Helper()
	t.Setenv("OKU_STATE_DIR", t.TempDir())
	j, err := journal.Open("main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func newSwapOrder(t *testing.T, j *journal.Journal, id string, mutate func(*journal.SwapOrder)) {
	t.Helper()
	o := journal.SwapOrder{ID: id, Agent: soAgent, ChainID: 42161, Token: soToken, Decimals: 18, Amount: "100", Size: "100", SlippageBps: 50, Basis: "1", BasisSource: "override", PeakValue: "1"}
	mutate(&o)
	if _, err := j.CreateSwapOrder(o); err != nil {
		t.Fatal(err)
	}
}

func tickAt(t *testing.T, a *App, j *journal.Journal, f *fakeSwapExec, x swapOrderExec, price string, dry bool) *swapOrderTick {
	t.Helper()
	f.price = price
	tick, err := a.swapOrdersTick(j, x, soAgent, dry)
	if err != nil {
		t.Fatal(err)
	}
	return tick
}

func orderState(t *testing.T, j *journal.Journal, id string) *journal.SwapOrder {
	t.Helper()
	o, err := j.GetSwapOrder(id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestSwapOrderTriggerLevels(t *testing.T) {
	o := &journal.SwapOrder{Basis: "1", TakeProfitPct: "50", StopLossPct: "20", TrailingStopPct: "10"}
	for _, tc := range []struct{ value, peak, want string }{
		{"1", "1", ""},
		{"1.49", "1.49", ""},
		{"1.5", "1.5", "take_profit"},
		{"0.8", "0.85", "stop_loss"},
		{"0.81", "0.85", ""},
		{"1.35", "1.5", "trailing_stop"},
		{"1.36", "1.5", ""},
		// Stop-loss wins when both fire.
		{"0.5", "1.5", "stop_loss"},
	} {
		if got := swapOrderTrigger(o, ratOf(tc.value), ratOf(tc.peak)); got != tc.want {
			t.Errorf("value %s peak %s: got %q want %q", tc.value, tc.peak, got, tc.want)
		}
	}
	trailOnly := &journal.SwapOrder{TrailingStopPct: "10"}
	if got := swapOrderTrigger(trailOnly, ratOf("0.1"), ratOf("1")); got != "trailing_stop" {
		t.Errorf("trailing without basis: %q", got)
	}
}

func TestSwapOrderTakeProfitSellsOnce(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{result: "confirmed"}
	x := f.exec()
	newSwapOrder(t, j, "so-tp", func(o *journal.SwapOrder) { o.TakeProfitPct = "50" })
	// size 100, basis 1 native: price 0.012 -> value 1.2, below +50%.
	tickAt(t, a, j, f, x, "0.012", false)
	if f.prepares != 0 || orderState(t, j, "so-tp").State != journal.SwapOrderActive {
		t.Fatal("sold below take-profit")
	}
	tick := tickAt(t, a, j, f, x, "0.015", false)
	o := orderState(t, j, "so-tp")
	if f.prepares != 1 || f.submits != 1 || o.State != journal.SwapOrderFilled || o.Trigger != "take_profit" || o.SellIntentID != "so-tp-sell" {
		t.Fatalf("take-profit: %+v prepares=%d submits=%d", o, f.prepares, f.submits)
	}
	if len(tick.Orders) != 1 || tick.Orders[0].SellIntentID != "so-tp-sell" || tick.Active != 0 {
		t.Fatalf("tick: %+v", tick)
	}
	tickAt(t, a, j, f, x, "0.02", false)
	if f.prepares != 1 || f.submits != 1 {
		t.Fatal("filled order sold again")
	}
}

func TestSwapOrderStopLoss(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{result: "confirmed"}
	x := f.exec()
	newSwapOrder(t, j, "so-sl", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	tickAt(t, a, j, f, x, "0.0091", false)
	if f.prepares != 0 {
		t.Fatal("stop-loss fired early")
	}
	tickAt(t, a, j, f, x, "0.009", false)
	if o := orderState(t, j, "so-sl"); o.State != journal.SwapOrderFilled || o.Trigger != "stop_loss" || f.submits != 1 {
		t.Fatalf("stop-loss: %+v", o)
	}
}

func TestSwapOrderTrailingPeakRatchetsUpOnly(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{result: "confirmed"}
	x := f.exec()
	newSwapOrder(t, j, "so-trail", func(o *journal.SwapOrder) { o.TrailingStopPct = "10"; o.Basis = ""; o.BasisSource = "none" })
	for _, step := range []struct{ price, peak string }{{"0.01", "1"}, {"0.02", "2"}, {"0.019", "2"}, {"0.025", "2.5"}, {"0.023", "2.5"}} {
		tick := tickAt(t, a, j, f, x, step.price, false)
		if got := orderState(t, j, "so-trail").PeakValue; got != step.peak || tick.Orders[0].Peak != step.peak {
			t.Fatalf("price %s: peak %s want %s", step.price, got, step.peak)
		}
	}
	if f.prepares != 0 {
		t.Fatal("trailing stop fired within 10% of the peak")
	}
	tickAt(t, a, j, f, x, "0.0225", false)
	if o := orderState(t, j, "so-trail"); o.State != journal.SwapOrderFilled || o.Trigger != "trailing_stop" || o.PeakValue != "2.5" {
		t.Fatalf("trailing: %+v", o)
	}
}

func TestSwapOrderDryRunLeavesJournalUnchanged(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{result: "confirmed"}
	x := f.exec()
	newSwapOrder(t, j, "so-dry", func(o *journal.SwapOrder) { o.StopLossPct = "10"; o.TrailingStopPct = "5" })
	before := orderState(t, j, "so-dry")
	tick := tickAt(t, a, j, f, x, "0.005", true)
	if f.prepares != 0 || f.submits != 0 || !tick.Orders[0].WouldTrigger || tick.Orders[0].Trigger != "stop_loss" || tick.Orders[0].Peak != "1" || tick.Orders[0].Value != "0.5" {
		t.Fatalf("dry run: %+v", tick.Orders)
	}
	if after := orderState(t, j, "so-dry"); *after != *before {
		t.Fatalf("dry run changed the journal:\n%+v\n%+v", before, after)
	}
}

func TestSwapOrderUnknownSubmitNeverRetried(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{result: "unknown", submitErr: output.Errorf(output.CodeOutcomeUnknown, "timeout")}
	x := f.exec()
	newSwapOrder(t, j, "so-unknown", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	tickAt(t, a, j, f, x, "0.001", false)
	o := orderState(t, j, "so-unknown")
	if o.State != journal.SwapOrderFailed || !o.NeedsReconcile || !strings.Contains(o.LastError, "never retried") {
		t.Fatalf("unknown: %+v", o)
	}
	tickAt(t, a, j, f, x, "0.001", false)
	if f.submits != 1 || f.prepares != 1 {
		t.Fatal("unknown outcome was retried")
	}
}

func TestSwapOrderPrepareFailureRetriesThenGivesUp(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{prepareErr: output.Errorf(output.CodeRejected, "high impact")}
	x := f.exec()
	newSwapOrder(t, j, "so-prep", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	for i := 1; i <= maxSwapOrderAttempts+2; i++ {
		tickAt(t, a, j, f, x, "0.001", false)
	}
	o := orderState(t, j, "so-prep")
	if f.prepares != maxSwapOrderAttempts || f.submits != 0 || o.State != journal.SwapOrderFailed || o.NeedsReconcile || o.Attempts != maxSwapOrderAttempts {
		t.Fatalf("prepare failures: %+v prepares=%d", o, f.prepares)
	}
}

func TestSwapOrderOneSellPerTick(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{result: "confirmed"}
	x := f.exec()
	newSwapOrder(t, j, "so-a", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	newSwapOrder(t, j, "so-b", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	tick := tickAt(t, a, j, f, x, "0.001", false)
	if f.submits != 1 || orderState(t, j, "so-b").State != journal.SwapOrderActive || tick.Active != 1 {
		t.Fatalf("two sells in one tick: submits=%d %+v", f.submits, tick.Orders)
	}
	tickAt(t, a, j, f, x, "0.001", false)
	if f.submits != 2 || orderState(t, j, "so-b").State != journal.SwapOrderFilled {
		t.Fatal("deferred order not sold on the next tick")
	}
}

func TestSwapOrderResumeExecutingNeverResubmits(t *testing.T) {
	j := openSwapJournal(t)
	a := &App{Now: time.Now}
	f := &fakeSwapExec{}
	x := f.exec()
	newSwapOrder(t, j, "so-crash", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	trigger, intent := "stop_loss", "so-crash-sell"
	if err := j.TransitionSwapOrder("so-crash", journal.SwapOrderActive, journal.SwapOrderTriggered, journal.SwapOrderPatch{Trigger: &trigger}); err != nil {
		t.Fatal(err)
	}
	if err := j.TransitionSwapOrder("so-crash", journal.SwapOrderTriggered, journal.SwapOrderExecuting, journal.SwapOrderPatch{SellIntentID: &intent}); err != nil {
		t.Fatal(err)
	}
	// A watcher died mid-submit: the sell intent is still sending.
	f.states[intent] = "sending"
	tickAt(t, a, j, f, x, "0.001", false)
	o := orderState(t, j, "so-crash")
	if f.prepares != 0 || f.submits != 0 || o.State != journal.SwapOrderFailed || !o.NeedsReconcile {
		t.Fatalf("resume: %+v", o)
	}
}

func TestSwapOrderCancelCommand(t *testing.T) {
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	f := &config.File{Version: 1, DefaultProfile: "main", Profiles: map[string]*config.Profile{"main": {Network: "mainnet", APIURL: config.DefaultAPIURL, Account: acct}}}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	j := openSwapJournal(t)
	newSwapOrder(t, j, "so-live", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	newSwapOrder(t, j, "so-busy", func(o *journal.SwapOrder) { o.StopLossPct = "10" })
	trigger, intent := "stop_loss", "so-busy-sell"
	_ = j.TransitionSwapOrder("so-busy", journal.SwapOrderActive, journal.SwapOrderTriggered, journal.SwapOrderPatch{Trigger: &trigger})
	_ = j.TransitionSwapOrder("so-busy", journal.SwapOrderTriggered, journal.SwapOrderExecuting, journal.SwapOrderPatch{SellIntentID: &intent})
	run := func(args ...string) map[string]any {
		var out bytes.Buffer
		a := &App{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader(""), Getenv: func(string) string { return "" }, Now: time.Now, Sleep: sleepCtx}
		a.Run(args)
		var env map[string]any
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("%v: %s", args, out.String())
		}
		return env
	}
	env := run("swap", "orders", "cancel", "--order", "so-busy", "--json")
	if env["ok"] != false || env["error"].(map[string]any)["code"] != "INTENT_STATE" || env["command"] != "swap.orders.cancel" {
		t.Fatalf("cancel while executing: %v", env)
	}
	if env = run("swap", "orders", "cancel", "--order", "so-live", "--json"); env["ok"] != true || env["data"].(map[string]any)["state"] != "cancelled" {
		t.Fatalf("cancel: %v", env)
	}
	if env = run("swap", "orders", "cancel", "--order", "so-live", "--json"); env["ok"] != false {
		t.Fatalf("second cancel: %v", env)
	}
	if env = run("swap", "orders", "cancel", "--json"); env["ok"] != false || env["error"].(map[string]any)["code"] != "USAGE" {
		t.Fatalf("missing --order: %v", env)
	}
	env = run("swap", "orders", "list", "--state", "executing", "--json")
	orders := env["data"].(map[string]any)["orders"].([]any)
	if len(orders) != 1 || orders[0].(map[string]any)["order_id"] != "so-busy" || orders[0].(map[string]any)["sell_intent_id"] != intent {
		t.Fatalf("list: %v", env)
	}
	if env = run("swap", "orders", "list", "--state", "bogus", "--json"); env["ok"] != false || env["error"].(map[string]any)["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("bad state enum: %v", env)
	}
}

func TestThreeWordCommandPaths(t *testing.T) {
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		a := &App{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader(""), Getenv: func(string) string { return "" }, Now: time.Now, Sleep: sleepCtx}
		return a.Run(args), out.String()
	}
	code, help := run("swap", "orders", "create", "--help")
	if code != 0 || !strings.Contains(help, "oku swap orders create") || !strings.Contains(help, "--trailing-stop-pct") {
		t.Fatalf("3-word help: %d %s", code, help)
	}
	if code, help = run("swap", "--help"); code != 0 || !strings.Contains(help, "orders") || !strings.Contains(help, "prepare") {
		t.Fatalf("swap help: %s", help)
	}
	for _, tc := range []struct {
		args    []string
		command string
	}{
		{[]string{"swap", "orders", "bogus", "--json"}, "swap.orders.bogus"},
		{[]string{"swap", "orders", "--json"}, "swap.orders"},
		{[]string{"swap", "bogus", "--json"}, "swap.bogus"},
		{[]string{"swap", "orders", "watch", "--interval", "nope", "--json"}, "swap.orders.watch"},
	} {
		code, out := run(tc.args...)
		var env map[string]any
		_ = json.Unmarshal([]byte(out), &env)
		if code != output.CodeUsage.ExitCode() || env["command"] != tc.command || env["error"].(map[string]any)["code"] != "USAGE" {
			t.Errorf("%v: code %d %s", tc.args, code, out)
		}
	}
	if got := (&App{}).commandName([]string{"--profile", "p", "swap", "orders", "watch", "--once"}); got != "swap.orders.watch" {
		t.Errorf("commandName: %s", got)
	}
	if got := (&App{}).commandName([]string{"swap", "status"}); got != "swap.status" {
		t.Errorf("commandName 2-word: %s", got)
	}
	_, ref := run("docs", "commands", "--markdown")
	for _, sub := range []string{"create", "list", "cancel", "watch"} {
		if !strings.Contains(ref, "## `oku swap orders "+sub+"`") {
			t.Errorf("reference missing swap orders %s", sub)
		}
	}
	if strings.Contains(ref, "## `oku swap create`") || strings.Contains(ref, "## `oku swap orders`\n") {
		t.Error("reference flattened 3-word commands")
	}
}

func TestTokenPricesNativeRatio(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "TokenService/GetTokenPrice") {
			t.Errorf("unexpected call %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"results":[{"chain":"eip155:42161","priceUsd":2000},{"chain":"eip155:42161","address":"` + strings.ToLower(soToken) + `","priceUsd":0.5},{"chain":"eip155:42161","address":"0x2222222222222222222222222222222222222222","error":"no price"}]}`))
	}))
	defer srv.Close()
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	f := &config.File{Version: 1, DefaultProfile: "main", Profiles: map[string]*config.Profile{"main": {Network: "mainnet", APIURL: srv.URL, Account: acct}}}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	a := &App{Getenv: func(string) string { return "" }, Version: "test"}
	e := &env{app: a, ctx: context.Background(), g: globals{timeout: 5 * time.Second}}
	prices, err := a.tokenPrices(e, 42161, []string{soToken, "0x2222222222222222222222222222222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	if p := prices[strings.ToLower(soToken)]; p == nil || p.Cmp(big.NewRat(1, 4000)) != 0 {
		t.Fatalf("ratio: %v", p)
	}
	if _, ok := prices["0x2222222222222222222222222222222222222222"]; ok {
		t.Fatal("errored price used")
	}
	tokens, _ := got["tokens"].([]any)
	if len(tokens) != 3 || tokens[0].(map[string]any)["address"] != eip7528Address || tokens[0].(map[string]any)["chain"] != "42161" {
		t.Fatalf("request: %v", got)
	}
}

func TestSwapOrderWatchRefusesUnboundedInProcess(t *testing.T) {
	s := NewService("test", &bytes.Buffer{}, func(string) string { return "" })
	s.AllowMutations(true)
	_, err := s.Call(context.Background(), "swap orders watch", Globals{}, map[string]any{}, nil)
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != output.CodeUsage || !strings.Contains(oe.Message, "once") {
		t.Fatalf("in-process watch: %v", err)
	}
}
