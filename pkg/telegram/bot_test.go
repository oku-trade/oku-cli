package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/output"
)

const (
	testToken = "123456:SECRET-test-token-value"
	alice     = int64(1001)
	mallory   = int64(6666)
)

type tgCall struct {
	method string
	body   map[string]any
}

// fakeTelegram is an httptest Bot API. getUpdates serves queued batches.
type fakeTelegram struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []tgCall
	nextMsg int64
	stale   []Update
	updates chan []Update
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{t: t, nextMsg: 100, updates: make(chan []Update, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + testToken + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, `{"ok":false,"error_code":404,"description":"Not Found"}`, http.StatusNotFound)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, prefix)
		var body map[string]any
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		_ = dec.Decode(&body)
		f.mu.Lock()
		f.calls = append(f.calls, tgCall{method, body})
		f.mu.Unlock()
		switch method {
		case "getUpdates":
			var batch []Update
			if fmt.Sprint(body["offset"]) == "-1" {
				batch = f.stale
			} else {
				select {
				case batch = <-f.updates:
				case <-time.After(50 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
			raw, _ := json.Marshal(batch)
			if batch == nil {
				raw = []byte("[]")
			}
			fmt.Fprintf(w, `{"ok":true,"result":%s}`, raw)
		case "sendMessage":
			f.mu.Lock()
			f.nextMsg++
			id := f.nextMsg
			f.mu.Unlock()
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":%s,"type":"private"}}}`, id, body["chat_id"])
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) byMethod(method string) []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTelegram) replies() []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgCall
	for _, c := range f.calls {
		if c.method != "getUpdates" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTelegram) last(method string) tgCall {
	calls := f.byMethod(method)
	if len(calls) == 0 {
		f.t.Fatalf("no %s call", method)
	}
	return calls[len(calls)-1]
}

func (f *fakeTelegram) allText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(f.calls)
	var sb strings.Builder
	sb.Write(raw)
	for _, c := range f.calls {
		b, _ := json.Marshal(c.body)
		sb.Write(b)
	}
	return sb.String()
}

type beCall struct {
	path  string
	flags map[string]any
}

// fakeBackend records calls and answers from per-path handlers.
type fakeBackend struct {
	mu       sync.Mutex
	calls    []beCall
	handlers map[string]func(map[string]any) (any, error)
	has      map[string]bool
	delay    time.Duration
}

func (f *fakeBackend) Call(_ context.Context, path string, opts map[string]any, _ []string) (any, error) {
	cp := map[string]any{}
	for k, v := range opts {
		cp[k] = v
	}
	f.mu.Lock()
	f.calls = append(f.calls, beCall{path, cp})
	h := f.handlers[path]
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if h == nil {
		return map[string]any{}, nil
	}
	return h(opts)
}

func (f *fakeBackend) Has(path string) bool { return f.has[path] }

func (f *fakeBackend) callsTo(path string) []beCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []beCall
	for _, c := range f.calls {
		if c.path == path {
			out = append(out, c)
		}
	}
	return out
}

type clock struct{ ns atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }
func newClock() *clock {
	c := &clock{}
	c.ns.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func privateMsg(user int64, text string) *Message {
	return &Message{MessageID: 1, From: &User{ID: user}, Chat: Chat{ID: user, Type: "private"}, Text: text}
}
func tap(user, msg int64, data string) *CallbackQuery {
	return &CallbackQuery{ID: "cq", From: User{ID: user}, Message: &Message{MessageID: msg, Chat: Chat{ID: user, Type: "private"}}, Data: data}
}

type harness struct {
	t   *testing.T
	tg  *fakeTelegram
	be  *fakeBackend
	clk *clock
	bot *bot
	log *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newHarness(t *testing.T) *harness {
	tg := newFakeTelegram(t)
	be := &fakeBackend{handlers: map[string]func(map[string]any) (any, error){}, has: map[string]bool{}}
	clk := newClock()
	log := &syncBuffer{}
	b := newBot(Config{Token: testToken, BaseURL: tg.srv.URL, Backend: be, AllowedUserIDs: []int64{alice}, Profile: "main", Log: log, Now: clk.now})
	return &harness{t: t, tg: tg, be: be, clk: clk, bot: b, log: log}
}

const (
	preparedIntent = "oku-aaaabbbbccccdddd"
	preparedDigest = "d1e2a3d4b5e6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f70"
)

func (h *harness) stubSwapPrepare(highImpact bool) {
	h.be.handlers["swap prepare"] = func(map[string]any) (any, error) {
		return map[string]any{
			"intent_id": preparedIntent, "digest": preparedDigest, "state": "prepared", "kind": "swap",
			"chain_id": 42161, "recipient": "0x00000000000000000000000000000000000000aa", "route": "oku",
			"min_out": "1000", "expires_at": h.clk.now().Add(10 * time.Minute).Format(time.RFC3339Nano),
			"valuation": map[string]any{"in_usd": 10.0, "out_usd": 9.9, "loss_bps": 10, "high_impact": highImpact},
		}, nil
	}
	h.be.handlers["swap submit"] = func(map[string]any) (any, error) {
		return map[string]any{"state": "confirmed", "tx_hashes": []any{"0xhash"}}, nil
	}
}

// startSwap taps Swap, sends the one-line input and returns the summary
// message ID and its pending action ID.
func (h *harness) startSwap() (int64, string) {
	ctx := context.Background()
	h.bot.onCallback(ctx, tap(alice, 50, "m:swap"))
	h.bot.onMessage(ctx, privateMsg(alice, "42161 native usdc 0.01 50"))
	sent := h.tg.last("sendMessage")
	msg := h.lastSentID()
	edit := h.tg.last("editMessageText")
	if fmt.Sprint(edit.body["message_id"]) != fmt.Sprint(msg) || sent.body == nil {
		h.t.Fatalf("summary did not edit the prepare message")
	}
	id := ""
	for _, b := range buttons(edit.body) {
		if strings.HasPrefix(b, "c:") {
			id = strings.TrimPrefix(b, "c:")
		}
	}
	return msg, id
}

func (h *harness) lastSentID() int64 {
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	return h.tg.nextMsg
}

func buttons(body map[string]any) []string {
	var out []string
	rm, _ := body["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			out = append(out, b.(map[string]any)["callback_data"].(string))
		}
	}
	return out
}

func labels(body map[string]any) []string {
	var out []string
	rm, _ := body["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			out = append(out, b.(map[string]any)["text"].(string))
		}
	}
	return out
}

func text(c tgCall) string { s, _ := c.body["text"].(string); return s }

func TestAuthorizeIgnoresStrangersAndGroups(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cases := []Update{
		{UpdateID: 1, Message: privateMsg(mallory, "hi")},
		{UpdateID: 2, Message: &Message{From: &User{ID: alice}, Chat: Chat{ID: -500, Type: "group"}, Text: "hi"}},
		{UpdateID: 3, Message: &Message{From: &User{ID: alice}, Chat: Chat{ID: mallory, Type: "private"}, Text: "hi"}},
		{UpdateID: 4, CallbackQuery: tap(mallory, 9, "m:wallet")},
		{UpdateID: 5, CallbackQuery: &CallbackQuery{ID: "x", From: User{ID: alice}, Message: &Message{MessageID: 9, Chat: Chat{ID: -1, Type: "supergroup"}}, Data: "m:wallet"}},
		{UpdateID: 6, Message: &Message{Chat: Chat{ID: alice, Type: "private"}, Text: "no from"}},
	}
	for _, u := range cases {
		h.bot.dispatch(ctx, u)
	}
	time.Sleep(50 * time.Millisecond)
	if r := h.tg.replies(); len(r) != 0 {
		t.Fatalf("replied to a stranger: %v", r)
	}
	if len(h.be.calls) != 0 {
		t.Fatalf("backend called for a stranger: %v", h.be.calls)
	}
	if strings.Contains(h.log.String(), "hi") || strings.Contains(h.log.String(), fmt.Sprint(mallory)) {
		t.Fatalf("log leaked content: %q", h.log.String())
	}
}

func TestServeRefusesWithoutAllowlistOrToken(t *testing.T) {
	tg := newFakeTelegram(t)
	be := &fakeBackend{}
	if err := Serve(context.Background(), Config{Token: testToken, BaseURL: tg.srv.URL, Backend: be}); err != ErrNoAllowlist {
		t.Fatalf("want ErrNoAllowlist, got %v", err)
	}
	if err := Serve(context.Background(), Config{Token: testToken, BaseURL: tg.srv.URL, Backend: be, AllowedUserIDs: []int64{0, -5}}); err != ErrNoAllowlist {
		t.Fatalf("want ErrNoAllowlist for non-positive IDs, got %v", err)
	}
	if err := Serve(context.Background(), Config{BaseURL: tg.srv.URL, Backend: be, AllowedUserIDs: []int64{alice}}); err != ErrNoToken {
		t.Fatalf("want ErrNoToken, got %v", err)
	}
	if n := len(tg.byMethod("getUpdates")); n != 0 {
		t.Fatalf("contacted Telegram %d times", n)
	}
}

func TestServeDrainsStaleUpdatesAndIgnoresStrangers(t *testing.T) {
	tg := newFakeTelegram(t)
	tg.stale = []Update{{UpdateID: 41, Message: privateMsg(alice, "/start")}}
	be := &fakeBackend{handlers: map[string]func(map[string]any) (any, error){}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{Token: testToken, BaseURL: tg.srv.URL, Backend: be, AllowedUserIDs: []int64{alice}})
	}()
	tg.updates <- []Update{{UpdateID: 42, Message: privateMsg(mallory, "/start")}, {UpdateID: 43, Message: privateMsg(alice, "/start")}}
	deadline := time.Now().Add(3 * time.Second)
	for len(tg.byMethod("sendMessage")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sends := tg.byMethod("sendMessage")
	if len(sends) != 1 || fmt.Sprint(sends[0].body["chat_id"]) != fmt.Sprint(alice) {
		t.Fatalf("want exactly one reply to alice, got %v", sends)
	}
	polls := tg.byMethod("getUpdates")
	if fmt.Sprint(polls[0].body["offset"]) != "-1" || fmt.Sprint(polls[1].body["offset"]) != "42" {
		t.Fatalf("stale updates not skipped: %v %v", polls[0].body, polls[1].body)
	}
}

func TestTokenNeverLeaks(t *testing.T) {
	// Transport error: url.Error carries the full URL including the token.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	c := NewClient(deadURL, testToken)
	_, err := c.GetUpdates(context.Background(), 0, 0)
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), deadURL) {
		t.Fatalf("net error leaks: %v", err)
	}
	// API error whose description echoes the token.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":"bad request for %s"}`, r.URL.Path)
	}))
	defer echo.Close()
	_, err = NewClient(echo.URL, testToken).SendMessage(context.Background(), 1, "x", nil)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("api error leaks: %v", err)
	}
	// Serve logs a failing poll without the token.
	log := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{Token: testToken, BaseURL: deadURL, Backend: &fakeBackend{}, AllowedUserIDs: []int64{alice}, Log: log})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(log.String(), "getUpdates failed") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(log.String(), "getUpdates failed") || strings.Contains(log.String(), testToken) || strings.Contains(log.String(), "SECRET") {
		t.Fatalf("log: %q", log.String())
	}
	// Backend errors with secrets in message or details are not forwarded.
	h := newHarness(t)
	h.be.handlers["wallet address"] = func(map[string]any) (any, error) {
		return nil, output.Errorf(output.CodeAuthRequired, "token %s at /home/u/.config/oku https://x/bot%s", testToken, testToken).WithDetail("hint", testToken)
	}
	h.bot.onCallback(context.Background(), tap(alice, 7, "m:wallet"))
	all := h.tg.allText() + h.log.String()
	if strings.Contains(all, testToken) || strings.Contains(all, "/home/u") {
		t.Fatalf("backend error leaked: %s", all)
	}
	if !strings.Contains(text(h.tg.last("editMessageText")), "oku auth login") {
		t.Fatalf("missing curated hint: %q", text(h.tg.last("editMessageText")))
	}
}

func TestMenuEditsInPlaceWithRefreshAndBack(t *testing.T) {
	h := newHarness(t)
	h.be.handlers["wallet address"] = func(map[string]any) (any, error) {
		return map[string]any{"agent": "0xagent", "master": "0xmaster"}, nil
	}
	h.be.handlers["wallet balances"] = func(map[string]any) (any, error) {
		return map[string]any{"native": map[string]any{"eip155:42161": map[string]any{"balance": "0.5", "symbol": "ETH"}}}, nil
	}
	ctx := context.Background()
	for _, data := range []string{"m:wallet", "m:wallet", "m:main", "m:perps", "m:positions", "m:orders", "m:fills", "m:pnl", "m:status", "m:exits", "m:bridge", "m:fund", "m:wallet_all"} {
		h.bot.onCallback(ctx, tap(alice, 77, data))
		e := h.tg.last("editMessageText")
		if fmt.Sprint(e.body["message_id"]) != "77" {
			t.Fatalf("%s: edited message %v", data, e.body["message_id"])
		}
		names := strings.Join(labels(e.body), " ")
		if data != "m:main" && (!strings.Contains(names, "Refresh") || !strings.Contains(names, "Back")) {
			t.Fatalf("%s: missing Refresh/Back: %s", data, names)
		}
		if data != "m:main" && !strings.Contains(strings.Join(buttons(e.body), " "), data) {
			t.Fatalf("%s: Refresh does not reload the screen", data)
		}
	}
	if len(h.tg.byMethod("sendMessage")) != 0 {
		t.Fatal("menu navigation sent new messages")
	}
	if !strings.Contains(text(h.tg.byMethod("editMessageText")[0]), "0.5 ETH") {
		t.Fatalf("wallet text: %q", text(h.tg.byMethod("editMessageText")[0]))
	}
	bal := h.be.callsTo("wallet balances")
	if bal[0].flags["chain"] != "42161" || bal[len(bal)-1].flags["all-chains"] != true {
		t.Fatalf("balances flags: %v", bal)
	}
	if fund := h.be.callsTo("wallet fund"); len(fund) != 1 || fund[0].flags["no-open"] != true {
		t.Fatalf("fund flags: %v", fund)
	}
}

func TestPnLButtonOnlyWhenCommandExists(t *testing.T) {
	h := newHarness(t)
	h.bot.onCallback(context.Background(), tap(alice, 5, "m:pnl"))
	if strings.Contains(strings.Join(buttons(h.tg.last("editMessageText").body), " "), "pnl_all") {
		t.Fatal("PnL button shown without a pnl command")
	}
	h.bot.onCallback(context.Background(), tap(alice, 5, "m:pnl_all"))
	if len(h.be.callsTo("pnl")) != 0 {
		t.Fatal("called pnl without the command")
	}
	h.be.has["pnl"] = true
	h.bot.onCallback(context.Background(), tap(alice, 5, "m:pnl"))
	if !strings.Contains(strings.Join(buttons(h.tg.last("editMessageText").body), " "), "pnl_all") {
		t.Fatal("PnL button missing with a pnl command")
	}
}

func TestCallbackDataFitsLimit(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("x", 48)
	h.be.handlers["swap orders list"] = func(map[string]any) (any, error) {
		return map[string]any{"orders": []any{map[string]any{"order_id": long, "state": "active", "token": "0xt", "chain_id": 42161}}}, nil
	}
	var orders []any
	for i := range 25 {
		orders = append(orders, map[string]any{"oid": fmt.Sprint(9223372036854775000 + i), "coin": "BTC", "side": "B", "sz": "1", "limitPx": "1"})
	}
	h.be.handlers["orders list"] = func(map[string]any) (any, error) { return map[string]any{"orders": orders}, nil }
	h.be.handlers["bridge status"] = func(map[string]any) (any, error) {
		return []any{map[string]any{"intent_id": "oku-" + strings.Repeat("b", 60), "state": "submitted", "claim_required": true}}, nil
	}
	h.stubSwapPrepare(false)
	ctx := context.Background()
	for _, data := range []string{"m:main", "m:exits", "m:orders", "m:orders:2", "m:bridge", "m:status", "so:" + long, "po:9223372036854775001", "cl"} {
		h.bot.onCallback(ctx, tap(alice, 1, data))
	}
	h.startSwap()
	for _, c := range h.tg.replies() {
		for _, b := range buttons(c.body) {
			if len(b) > maxCallbackData {
				t.Fatalf("callback_data %d bytes: %q", len(b), b)
			}
		}
	}
	if pages := text(h.tg.byMethod("editMessageText")[2]); !strings.Contains(pages, "page 1 of 3") {
		t.Fatalf("orders not paged: %q", pages)
	}
}

func TestSwapConfirmSubmitsPreparedIntentAndDigest(t *testing.T) {
	h := newHarness(t)
	h.stubSwapPrepare(false)
	msg, id := h.startSwap()
	if id == "" || len(id) != 32 {
		t.Fatalf("pending id %q", id)
	}
	prep := h.be.callsTo("swap prepare")
	if len(prep) != 1 || prep[0].flags["chain"] != "42161" || prep[0].flags["from"] != "native" || prep[0].flags["to"] != "usdc" || prep[0].flags["amount"] != "0.01" || prep[0].flags["slippage-bps"] != int64(50) || len(prep[0].flags) != 5 {
		t.Fatalf("prepare flags: %v", prep)
	}
	summary := text(h.tg.last("editMessageText"))
	for _, want := range []string{"Route: oku", "Minimum out (raw): 1000", "$10.00 in", "loss 10 bps", "d1e2a3d4...", "Quote expires"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %s", want, summary)
		}
	}
	// Text typed after the summary never reaches Confirm.
	h.bot.onMessage(context.Background(), privateMsg(alice, "42161 native usdc 999 0xdeadbeef"))
	h.bot.onCallback(context.Background(), tap(alice, msg, "c:"+id))
	sub := h.be.callsTo("swap submit")
	if len(sub) != 1 || len(sub[0].flags) != 2 || sub[0].flags["intent"] != preparedIntent || sub[0].flags["digest"] != preparedDigest {
		t.Fatalf("submit flags: %v", sub)
	}
	if len(h.be.callsTo("swap prepare")) != 1 {
		t.Fatal("stray text started a new prepare")
	}
	final := h.tg.last("editMessageText")
	if !strings.Contains(text(final), "confirmed") || !strings.Contains(text(final), "0xhash") || fmt.Sprint(final.body["message_id"]) != fmt.Sprint(msg) {
		t.Fatalf("final: %v", final.body)
	}
	for _, e := range h.tg.byMethod("editMessageText") {
		if strings.Contains(text(e), "Working") {
			return
		}
	}
	t.Fatal("no Working... edit before submit")
}

func TestDoubleConfirmSubmitsOnce(t *testing.T) {
	h := newHarness(t)
	h.stubSwapPrepare(false)
	h.be.delay = 0
	msg, id := h.startSwap()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.bot.confirm(context.Background(), alice, alice, msg, id)
		}()
	}
	wg.Wait()
	if n := len(h.be.callsTo("swap submit")); n != 1 {
		t.Fatalf("submitted %d times", n)
	}
	h.bot.onCallback(context.Background(), tap(alice, msg, "c:"+id))
	if n := len(h.be.callsTo("swap submit")); n != 1 || !strings.Contains(text(h.tg.last("editMessageText")), "already used") {
		t.Fatalf("replay: %d %q", n, text(h.tg.last("editMessageText")))
	}
}

func TestConfirmExpires(t *testing.T) {
	h := newHarness(t)
	h.stubSwapPrepare(false)
	msg, id := h.startSwap()
	h.clk.advance(pendingTTL)
	h.bot.onCallback(context.Background(), tap(alice, msg, "c:"+id))
	if len(h.be.callsTo("swap submit")) != 0 || !strings.Contains(text(h.tg.last("editMessageText")), "expired") {
		t.Fatalf("expired confirm ran: %q", text(h.tg.last("editMessageText")))
	}
}

func TestConfirmExpiresWithQuote(t *testing.T) {
	h := newHarness(t)
	h.stubSwapPrepare(false)
	inner := h.be.handlers["swap prepare"]
	h.be.handlers["swap prepare"] = func(o map[string]any) (any, error) {
		r, _ := inner(o)
		r.(map[string]any)["expires_at"] = h.clk.now().Add(30 * time.Second).Format(time.RFC3339Nano)
		return r, nil
	}
	msg, id := h.startSwap()
	h.clk.advance(31 * time.Second)
	h.bot.onCallback(context.Background(), tap(alice, msg, "c:"+id))
	if len(h.be.callsTo("swap submit")) != 0 {
		t.Fatal("confirmed after the quote expired")
	}
}

func TestConfirmWrongUserOrMessage(t *testing.T) {
	h := newHarness(t)
	h.bot.allowed[mallory] = true
	h.stubSwapPrepare(false)
	msg, id := h.startSwap()
	h.bot.onCallback(context.Background(), tap(alice, msg+1, "c:"+id))
	h.bot.onCallback(context.Background(), tap(mallory, msg, "c:"+id))
	if len(h.be.callsTo("swap submit")) != 0 {
		t.Fatal("confirm accepted from the wrong user or message")
	}
	h.bot.onCallback(context.Background(), tap(alice, msg, "c:"+id))
	if len(h.be.callsTo("swap submit")) != 1 {
		t.Fatal("the bound confirm was consumed by a mismatched tap")
	}
}

func TestCancelAbandons(t *testing.T) {
	h := newHarness(t)
	h.stubSwapPrepare(false)
	msg, id := h.startSwap()
	h.bot.onCallback(context.Background(), tap(alice, msg, "x:"+id))
	if got := text(h.tg.last("editMessageText")); !strings.Contains(got, "Cancelled. Nothing was sent") || !strings.Contains(got, "prepared") {
		t.Fatalf("cancel text: %q", got)
	}
	h.bot.onCallback(context.Background(), tap(alice, msg, "c:"+id))
	if len(h.be.callsTo("swap submit")) != 0 {
		t.Fatal("confirmed after cancel")
	}
}

func TestHighImpactNotOffered(t *testing.T) {
	h := newHarness(t)
	h.stubSwapPrepare(true)
	h.bot.onCallback(context.Background(), tap(alice, 50, "m:swap"))
	h.bot.onMessage(context.Background(), privateMsg(alice, "42161 native usdc 1"))
	e := h.tg.last("editMessageText")
	for _, b := range buttons(e.body) {
		if strings.HasPrefix(b, "c:") {
			t.Fatal("Confirm offered for a high impact swap")
		}
	}
	if !strings.Contains(text(e), "High price impact") {
		t.Fatalf("text: %q", text(e))
	}
	for _, c := range h.be.callsTo("swap prepare") {
		if _, ok := c.flags["allow-high-impact"]; ok {
			t.Fatal("bot passed allow-high-impact")
		}
	}
}

func TestSendMaxNeedsResolvedAmount(t *testing.T) {
	h := newHarness(t)
	h.be.handlers["send prepare"] = func(map[string]any) (any, error) {
		return map[string]any{"intent_id": preparedIntent, "digest": preparedDigest, "state": "prepared", "amount": "max", "chain_id": 42161, "token": "native"}, nil
	}
	h.bot.onCallback(context.Background(), tap(alice, 50, "m:send"))
	h.bot.onMessage(context.Background(), privateMsg(alice, "42161 native max"))
	for _, b := range buttons(h.tg.last("editMessageText").body) {
		if strings.HasPrefix(b, "c:") {
			t.Fatal("Confirm offered for max send without a resolved amount")
		}
	}
}

func TestOutcomeUnknownHasNoRetry(t *testing.T) {
	h := newHarness(t)
	h.stubSwapPrepare(false)
	h.be.handlers["swap submit"] = func(map[string]any) (any, error) {
		return nil, output.Errorf(output.CodeOutcomeUnknown, "rpc lost")
	}
	msg, id := h.startSwap()
	h.bot.onCallback(context.Background(), tap(alice, msg, "c:"+id))
	e := h.tg.last("editMessageText")
	if !strings.Contains(text(e), "Do not retry") || !strings.Contains(text(e), "oku swap status --intent "+preparedIntent) {
		t.Fatalf("text: %q", text(e))
	}
	if b := buttons(e.body); len(b) != 1 || b[0] != "m:main" {
		t.Fatalf("buttons: %v", b)
	}
}

func TestOneLineInputIsStrict(t *testing.T) {
	bad := map[string][]string{
		kindSwap:   {"", "42161 native usdc", "42161 native usdc 1 50 extra", "abc native usdc 1", "42161 native usdc -1", "42161 native usdc 1 301", "42161 native usdc 1\n42161", "42161 nat;ive usdc 1"},
		kindSend:   {"42161 usdc", "42161 usdc 1 notanaddress", "42161 usdc 1 0x1 extra"},
		kindBridge: {"42161 usdc usdc 1", "42161 x8453 usdc usdc 1"},
		kindExit:   {"42161 usdc all tp=5", "42161 0x0000000000000000000000000000000000000001 all", "42161 0x0000000000000000000000000000000000000001 all tp=5 tp=6", "42161 0x0000000000000000000000000000000000000001 all foo=1"},
	}
	for kind, inputs := range bad {
		for _, in := range inputs {
			if _, err := parseInput(kind, in); err == nil {
				t.Errorf("%s accepted %q", kind, in)
			}
		}
	}
	f, err := parseInput(kindBridge, "42161 8453 usdc usdc 10 30")
	if err != nil || f["destination-chain"] != "8453" || f["slippage-bps"] != int64(30) || len(f) != 6 {
		t.Fatalf("bridge: %v %v", f, err)
	}
	f, err = parseInput(kindExit, "42161 0x0000000000000000000000000000000000000001 ALL tp=50 sl=20")
	if err != nil || f["all"] != true || f["take-profit-pct"] != "50" || f["stop-loss-pct"] != "20" || len(f) != 5 {
		t.Fatalf("exit: %v %v", f, err)
	}
}

func TestExitCreateIsReviewedAndIdempotent(t *testing.T) {
	h := newHarness(t)
	h.be.handlers["swap orders create"] = func(o map[string]any) (any, error) {
		return map[string]any{"order_id": o["id"], "state": "active"}, nil
	}
	ctx := context.Background()
	h.bot.onCallback(ctx, tap(alice, 3, "m:exit_new"))
	h.bot.onMessage(ctx, privateMsg(alice, "42161 0x0000000000000000000000000000000000000001 5 trail=10"))
	e := h.tg.last("editMessageText")
	if len(h.be.callsTo("swap orders create")) != 0 || !strings.Contains(text(e), "swap orders watch") {
		t.Fatalf("exit created before Confirm or missing watch note: %q", text(e))
	}
	var id string
	for _, b := range buttons(e.body) {
		if strings.HasPrefix(b, "c:") {
			id = b[2:]
		}
	}
	h.bot.onCallback(ctx, tap(alice, h.lastSentID(), "c:"+id))
	c := h.be.callsTo("swap orders create")
	if len(c) != 1 || !strings.HasPrefix(fmt.Sprint(c[0].flags["id"]), "so-tg-") || c[0].flags["trailing-stop-pct"] != "10" || c[0].flags["amount"] != "5" {
		t.Fatalf("create: %v", c)
	}
}

func TestExitOrderTimeoutIsOutcomeUnknown(t *testing.T) {
	h := newHarness(t)
	h.be.handlers["swap orders create"] = func(map[string]any) (any, error) {
		return nil, output.Errorf(output.CodeUpstreamTimeout, "deadline exceeded")
	}
	ctx := context.Background()
	h.bot.onCallback(ctx, tap(alice, 3, "m:exit_new"))
	h.bot.onMessage(ctx, privateMsg(alice, "42161 0x0000000000000000000000000000000000000001 5 tp=10"))
	var id string
	for _, b := range buttons(h.tg.last("editMessageText").body) {
		if strings.HasPrefix(b, "c:") {
			id = b[2:]
		}
	}
	h.bot.onCallback(ctx, tap(alice, h.lastSentID(), "c:"+id))
	e := h.tg.last("editMessageText")
	if !strings.Contains(text(e), "Outcome unknown") || !strings.Contains(text(e), "oku swap orders list") || strings.Contains(text(e), "Try again") {
		t.Fatalf("text: %q", text(e))
	}
	if b := buttons(e.body); len(b) != 1 || b[0] != "m:main" {
		t.Fatalf("buttons: %v", b)
	}
}

func TestPerpsCancelBindsMarketAndOID(t *testing.T) {
	h := newHarness(t)
	h.be.handlers["orders list"] = func(map[string]any) (any, error) {
		return map[string]any{"orders": []any{map[string]any{"oid": "777", "coin": "ETH", "side": "A", "sz": "0.1", "limitPx": "4000"}}}, nil
	}
	ctx := context.Background()
	h.bot.onCallback(ctx, tap(alice, 9, "po:777"))
	e := h.tg.last("editMessageText")
	if !strings.Contains(text(e), "ETH") || !strings.Contains(text(e), "sell") || len(h.be.callsTo("orders cancel")) != 0 {
		t.Fatalf("summary: %q", text(e))
	}
	var id string
	for _, b := range buttons(e.body) {
		if strings.HasPrefix(b, "c:") {
			id = b[2:]
		}
	}
	h.bot.onCallback(ctx, tap(alice, 9, "c:"+id))
	c := h.be.callsTo("orders cancel")
	if len(c) != 1 || c[0].flags["market"] != "ETH" || c[0].flags["order-id"] != int64(777) || !strings.HasPrefix(fmt.Sprint(c[0].flags["intent-id"]), "tg-cancel-") {
		t.Fatalf("cancel: %v", c)
	}
	h.bot.onCallback(ctx, tap(alice, 9, "po:778"))
	if !strings.Contains(text(h.tg.last("editMessageText")), "no longer open") {
		t.Fatal("unknown oid offered for cancel")
	}
}

func TestClaimRunsPerShownIntent(t *testing.T) {
	h := newHarness(t)
	h.be.handlers["bridge status"] = func(map[string]any) (any, error) {
		return []any{
			map[string]any{"intent_id": "oku-1111", "state": "confirmed", "claim_required": true},
			map[string]any{"intent_id": "oku-2222", "state": "prepared", "claim_required": true},
			map[string]any{"intent_id": "oku-3333", "state": "confirmed", "claim_required": false},
		}, nil
	}
	ctx := context.Background()
	h.bot.onCallback(ctx, tap(alice, 4, "cl"))
	e := h.tg.last("editMessageText")
	var id string
	for _, b := range buttons(e.body) {
		if strings.HasPrefix(b, "c:") {
			id = b[2:]
		}
	}
	h.bot.onCallback(ctx, tap(alice, 4, "c:"+id))
	c := h.be.callsTo("bridge claim")
	if len(c) != 1 || c[0].flags["intent"] != "oku-1111" || c[0].flags["ready"] != nil {
		t.Fatalf("claims: %v", c)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	h := newHarness(t)
	h.be.handlers["wallet address"] = func(map[string]any) (any, error) { panic("boom " + testToken) }
	h.bot.handle(context.Background(), Update{CallbackQuery: tap(alice, 2, "m:wallet")})
	if got := text(h.tg.last("sendMessage")); got != genericError {
		t.Fatalf("panic reply: %q", got)
	}
	if strings.Contains(h.log.String(), testToken) || strings.Contains(h.log.String(), "boom") {
		t.Fatalf("panic logged details: %q", h.log.String())
	}
}

func TestCapText(t *testing.T) {
	s := strings.Repeat("a", 5000)
	got := capText(s)
	if utf16Len(got) > maxText || !strings.HasSuffix(got, truncatedNote) {
		t.Fatalf("len %d", utf16Len(got))
	}
	emoji := strings.Repeat("😀", 3000)
	if utf16Len(capText(emoji)) > maxText {
		t.Fatal("surrogate pairs overflow")
	}
}

func TestRetryAfter(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, testToken)
	var slept time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
	if err := c.AnswerCallbackQuery(context.Background(), "q"); err != nil || slept != 3*time.Second || n.Load() != 2 {
		t.Fatalf("err %v slept %s calls %d", err, slept, n.Load())
	}
}
