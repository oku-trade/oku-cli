package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/lending"
)

// loopFakeABI is what the fake Morpho chain answers: Morpho Blue market
// and position storage, the oracle, the IRM and ERC-20 reads.
const loopFakeABI = `[
{"name":"aggregate3","type":"function","inputs":[{"name":"calls","type":"tuple[]","components":[{"name":"target","type":"address"},{"name":"allowFailure","type":"bool"},{"name":"callData","type":"bytes"}]}],"outputs":[{"name":"returnData","type":"tuple[]","components":[{"name":"success","type":"bool"},{"name":"returnData","type":"bytes"}]}]},
{"name":"idToMarketParams","type":"function","inputs":[{"type":"bytes32"}],"outputs":[{"type":"address"},{"type":"address"},{"type":"address"},{"type":"address"},{"type":"uint256"}]},
{"name":"market","type":"function","inputs":[{"type":"bytes32"}],"outputs":[{"type":"uint128"},{"type":"uint128"},{"type":"uint128"},{"type":"uint128"},{"type":"uint128"},{"type":"uint128"}]},
{"name":"position","type":"function","inputs":[{"type":"bytes32"},{"type":"address"}],"outputs":[{"type":"uint256"},{"type":"uint128"},{"type":"uint128"}]},
{"name":"isAuthorized","type":"function","inputs":[{"type":"address"},{"type":"address"}],"outputs":[{"type":"bool"}]},
{"name":"decimals","type":"function","inputs":[],"outputs":[{"type":"uint8"}]},
{"name":"symbol","type":"function","inputs":[],"outputs":[{"type":"string"}]},
{"name":"allowance","type":"function","inputs":[{"type":"address"},{"type":"address"}],"outputs":[{"type":"uint256"}]},
{"name":"balanceOf","type":"function","inputs":[{"type":"address"}],"outputs":[{"type":"uint256"}]},
{"name":"price","type":"function","inputs":[],"outputs":[{"type":"uint256"}]},
{"name":"borrowRateView","type":"function","inputs":[{"type":"tuple","components":[{"name":"a","type":"address"},{"name":"b","type":"address"},{"name":"c","type":"address"},{"name":"d","type":"address"},{"name":"e","type":"uint256"}]},{"type":"tuple","components":[{"name":"a","type":"uint128"},{"name":"b","type":"uint128"},{"name":"c","type":"uint128"},{"name":"d","type":"uint128"},{"name":"e","type":"uint128"},{"name":"f","type":"uint128"}]}],"outputs":[{"type":"uint256"}]}
]`

var (
	loopLoan   = common.HexToAddress("0x1000000000000000000000000000000000000001") // 6 decimals
	loopColl   = common.HexToAddress("0x2000000000000000000000000000000000000002") // 18 decimals
	loopOracle = common.HexToAddress("0x3000000000000000000000000000000000000003")
	loopIrm    = common.HexToAddress("0x4000000000000000000000000000000000000004")
)

func loopMarket() lending.MarketParams {
	return lending.MarketParams{LoanToken: loopLoan, CollateralToken: loopColl, Oracle: loopOracle, Irm: loopIrm, Lltv: big.NewInt(860_000_000_000_000_000)}
}

func e18(v int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(v), big.NewInt(1_000_000_000_000_000_000))
}

// morphoLoopRPC fakes chain 1 with one market where collateral trades at
// *price loan per collateral (the test may move it). The agent holds `coll`
// collateral and owes `debtShares` borrow shares (1e6 shares per loan unit,
// 1:1 assets).
func morphoLoopRPC(t *testing.T, coll, debtShares *big.Int, authorized bool, price *big.Int) string {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(loopFakeABI))
	if err != nil {
		t.Fatal(err)
	}
	mp := loopMarket()
	// oracle = price * 1e36 * 1e6 / 1e18 (Morpho scales by loan/collateral decimals).
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)
	total := new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1_000_000)) // 1M loan units
	answer := func(to common.Address, data []byte) []byte {
		m, err := parsed.MethodById(data[:4])
		if err != nil {
			t.Errorf("unexpected selector %x", data[:4])
			return nil
		}
		var out []any
		switch m.Name {
		case "idToMarketParams":
			out = []any{mp.LoanToken, mp.CollateralToken, mp.Oracle, mp.Irm, mp.Lltv}
		case "market":
			// supply 1M, borrow 100k, shares = assets*1e6, updated now-ish.
			borrow := new(big.Int).Div(total, big.NewInt(10))
			out = []any{total, new(big.Int).Mul(total, big.NewInt(1_000_000)), borrow, new(big.Int).Mul(borrow, big.NewInt(1_000_000)), big.NewInt(1_900_000_000), big.NewInt(0)}
		case "position":
			out = []any{new(big.Int), debtShares, coll}
		case "isAuthorized":
			out = []any{authorized}
		case "decimals":
			out = []any{uint8(18)}
			if to == loopLoan {
				out = []any{uint8(6)}
			}
		case "symbol":
			out = []any{"COLL"}
			if to == loopLoan {
				out = []any{"USD"}
			}
		case "allowance":
			out = []any{new(big.Int)}
		case "balanceOf":
			out = []any{e18(100)}
		case "price":
			out = []any{new(big.Int).Mul(price, scale)}
		case "borrowRateView":
			out = []any{new(big.Int)}
		}
		b, err := m.Outputs.Pack(out...)
		if err != nil {
			t.Errorf("pack %s: %v", m.Name, err)
		}
		return b
	}
	type row struct {
		Success    bool
		ReturnData []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method == "eth_chainId" {
			fmt.Fprint(w, `{"result":"0x1"}`)
			return
		}
		if req.Method == "eth_simulateV1" {
			opts, _ := req.Params[0].(map[string]any)
			states, _ := opts["blockStateCalls"].([]any)
			sim, _ := states[0].(map[string]any)
			n := len(sim["calls"].([]any))
			out := make([]string, n)
			for i := range out {
				out[i] = `{"status":"0x1","returnData":"0x"}`
			}
			fmt.Fprintf(w, `{"result":[{"calls":[%s]}]}`, strings.Join(out, ","))
			return
		}
		if req.Method != "eth_call" {
			t.Errorf("prepare must only read: %s", req.Method)
			return
		}
		c, _ := req.Params[0].(map[string]any)
		data, _ := hex.DecodeString(strings.TrimPrefix(c["data"].(string), "0x"))
		args, err := parsed.Methods["aggregate3"].Inputs.Unpack(data[4:])
		if err != nil {
			t.Errorf("non-multicall eth_call: %v", err)
			return
		}
		calls := args[0].([]struct {
			Target       common.Address `json:"target"`
			AllowFailure bool           `json:"allowFailure"`
			CallData     []byte         `json:"callData"`
		})
		rows := make([]row, len(calls))
		for i, c := range calls {
			rows[i] = row{true, answer(c.Target, c.CallData)}
		}
		b, _ := parsed.Methods["aggregate3"].Outputs.Pack(rows)
		fmt.Fprintf(w, `{"result":"0x%x"}`, b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// routerFill encodes an Oku Router token-to-token fill.
func routerFill(t *testing.T, sell, buy, recipient common.Address, amount *big.Int) string {
	return routerFillValid(t, sell, buy, recipient, amount, 2_000_000_000)
}

// routerFillValid is routerFill with the warrant's validBefore set.
func routerFillValid(t *testing.T, sell, buy, recipient common.Address, amount *big.Int, validBefore int64) string {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(routerABI))
	if err != nil {
		t.Fatal(err)
	}
	type warrant struct {
		Nonce           *big.Int       `abi:"nonce"`
		ValidBefore     *big.Int       `abi:"validBefore"`
		ValidAfter      *big.Int       `abi:"validAfter"`
		VerifyingSigner common.Address `abi:"verifyingSigner"`
		Signature       []byte         `abi:"signature"`
	}
	data, err := parsed.Pack("fillQuoteTokenToToken", sell, buy, common.Address{9}, common.Address{9}, []byte{1}, amount, new(big.Int).Div(amount, big.NewInt(1000)), recipient, warrant{big.NewInt(1), big.NewInt(validBefore), new(big.Int), common.Address{7}, []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	return "0x" + hex.EncodeToString(data)
}

// loopSwapService answers one exact-in swap: the quote must name the
// bundle hop as both account and recipient, and the execution fill pays it.
func loopSwapService(t *testing.T, h *harness, sell, buy, payer, hop common.Address, minOut string, recipient *common.Address) {
	t.Helper()
	var amount string
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "GetSupportedMarkets":
			return map[string]any{"markets": []any{map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{1}}}}, true
		case "Create":
			p := body["params"].(map[string]any)
			if !strings.EqualFold(p["account"].(string), payer.Hex()) || !strings.EqualFold(p["dstAddress"].(string), hop.Hex()) || p["skipSimulation"] != true {
				t.Errorf("loop swap must be quoted for the router caller and delivered to the bundle hop without simulation: %v", p)
			}
			raw, err := parseUnits(p["tokenAmount"].(string), map[bool]int{true: 6, false: 18}[sell == loopLoan])
			if err != nil {
				t.Errorf("amount %v", p["tokenAmount"])
			}
			amount = raw.String()
			return map[string]any{"orderId": "01a15585-427e-70f2-a7e4-7457069b9c0c", "timeline": 1}, true
		case "GetFlowstreamQuotes":
			return map[string]any{"timeline": 1, "quotes": map[string]any{"zeroex": map[string]any{"router": "zeroex", "fetched": true, "quoteId": "q1", "quote": map[string]any{
				"chainId": 1, "isExactIn": true, "inAmountRaw": amount, "minAmountOutRaw": minOut, "outAmountRaw": minOut, "netUsdValue": 1, "priceImpact": -0.001,
				"inToken": map[string]any{"address": sell.Hex()}, "outToken": map[string]any{"address": buy.Hex()},
			}}}}, true
		case "UpdateQuoteSelect":
			return map[string]any{"router": "zeroex", "quoteId": "q1"}, true
		case "ConfirmQuoteSelect":
			return map[string]any{}, true
		case "GetExecutionInfo":
			to := hop
			if recipient != nil {
				to = *recipient
			}
			n, _ := new(big.Int).SetString(amount, 10)
			return map[string]any{"info": map[string]any{"trade": map[string]any{"chainId": "1", "to": okuRouter, "value": "0", "data": routerFill(t, sell, buy, to, n)}}}, true
		}
		return nil, false
	}
}

func loopHarness(t *testing.T, coll, debtShares *big.Int, authorized bool) *harness {
	h, _ := loopHarnessPriced(t, coll, debtShares, authorized)
	return h
}

// loopHarnessPriced also returns the fake oracle price to move.
func loopHarnessPriced(t *testing.T, coll, debtShares *big.Int, authorized bool) (*harness, *big.Int) {
	t.Helper()
	h := newHarness(t)
	h.enroll()
	price := big.NewInt(2000)
	h.rpcChain, h.rpcURL = "1", morphoLoopRPC(t, coll, debtShares, authorized, price)
	down := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	t.Cleanup(down.Close)
	h.extraEnv = map[string]string{"OKU_MORPHO_API_URL": down.URL}
	return h, price
}

func TestLoopOpenMorphoBindsVerifiedBundle(t *testing.T) {
	h := loopHarness(t, new(big.Int), new(big.Int), false)
	d, _ := lending.Morpho(1)
	// 1 COLL at 2000 USD, 3x at 50 bps: borrow 4000/1.01 = 3960.39 USD for
	// at least 1.97 COLL.
	loopSwapService(t, h, loopLoan, loopColl, d.Bundler3, d.GeneralAdapter1, "1970000000000000000", nil)
	id := loopMarket().ID().Hex()
	args := []string{"loop", "open", "--chain", "1", "--protocol", "morpho", "--market", id, "--deposit", "1", "--target-leverage", "3", "--json"}
	// 3x at 86% LLTV ends near HF 1.29: below 1.333 needs an explicit override.
	if r := h.run(args...); r.code == 0 || !strings.Contains(r.stdout, "high-risk threshold") {
		t.Fatalf("high-risk loop accepted without override: %s", r.stdout)
	}
	r := h.run(append(args, "--accept-high-risk")...)
	if r.code != 0 {
		t.Fatalf("loop open: %s %s", r.stdout, r.stderr)
	}
	data := r.data()
	plan := data["plan"].(map[string]any)
	expect := plan["expect"].(map[string]any)
	if expect["borrow:"+id] != "3960396039" || expect["supply:"+id] != "2970000000000000000" || expect["flash_loans"] != "1" || expect["authorize"] != "true" {
		t.Fatalf("loop open expectations: %v", expect)
	}
	// approve + authorize + bundle, all sealed into the digest.
	if n := len(data["templates"].([]any)); n != 3 {
		t.Fatalf("templates = %d, want approve, authorize and the bundle", n)
	}
	after := data["preview"].(map[string]any)["after"].(map[string]any)
	if lev := after["leverage"].(float64); lev < 2.98 || lev > 3.01 {
		t.Fatalf("projected leverage %v", lev)
	}
	if after["net_apy"] != nil {
		t.Fatalf("net APY must be N/A without rates, got %v", after["net_apy"])
	}
}

func TestLoopSwapPayingStrangerIsRefused(t *testing.T) {
	h := loopHarness(t, new(big.Int), new(big.Int), true)
	d, _ := lending.Morpho(1)
	stranger := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	loopSwapService(t, h, loopLoan, loopColl, d.Bundler3, d.GeneralAdapter1, "1990000000000000000", &stranger)
	r := h.run("loop", "open", "--chain", "1", "--protocol", "morpho", "--market", loopMarket().ID().Hex(), "--deposit", "1", "--target-leverage", "3", "--json")
	if r.code == 0 || r.errCode() != "PAYLOAD_MISMATCH" {
		t.Fatalf("fill paying a stranger accepted: %s", r.stdout)
	}
}

func TestLoopAdjustAndUnwindMorpho(t *testing.T) {
	// 3 COLL (6000 USD) against 3000 USD debt: 2x, LTV 50%.
	shares := new(big.Int).Mul(big.NewInt(3_000_000_000), big.NewInt(1_000_000))
	h := loopHarness(t, e18(3), shares, true)
	d, _ := lending.Morpho(1)
	id := loopMarket().ID().Hex()

	// Down to 1.5x: sell about 1/3 of value, repaying with the proceeds.
	loopSwapService(t, h, loopColl, loopLoan, d.Bundler3, d.GeneralAdapter1, "1990000000", nil)
	r := h.run("loop", "adjust", "--chain", "1", "--protocol", "morpho", "--market", id, "--target-leverage", "1.5", "--json")
	if r.code != 0 {
		t.Fatalf("loop adjust: %s %s", r.stdout, r.stderr)
	}
	expect := r.data()["plan"].(map[string]any)["expect"].(map[string]any)
	if expect["repay:"+id] != "true" || expect["flash_loans"] != "1" || expect["swaps"] != "1" {
		t.Fatalf("adjust-down expectations: %v", expect)
	}

	// Unwind into collateral: the swap must cover the whole debt.
	loopSwapService(t, h, loopColl, loopLoan, d.Bundler3, d.GeneralAdapter1, "3100000000", nil)
	u := h.run("loop", "unwind", "--chain", "1", "--protocol", "morpho", "--market", id, "--json")
	if u.code != 0 {
		t.Fatalf("loop unwind: %s %s", u.stdout, u.stderr)
	}
	ux := u.data()["plan"].(map[string]any)["expect"].(map[string]any)
	if ux["withdraw:"+id] != "3000000000000000000" || ux["repay:"+id] != "true" {
		t.Fatalf("unwind must withdraw all collateral and repay: %v", ux)
	}
	after := u.data()["preview"].(map[string]any)["after"].(map[string]any)
	if after["debt"] != "0 USD" {
		t.Fatalf("unwind must leave no debt: %v", after)
	}

	// A swap that cannot cover the debt is refused.
	loopSwapService(t, h, loopColl, loopLoan, d.Bundler3, d.GeneralAdapter1, "2000000000", nil)
	if s := h.run("loop", "unwind", "--chain", "1", "--protocol", "morpho", "--market", id, "--json"); s.code == 0 || !strings.Contains(s.stdout, "does not cover the debt") {
		t.Fatalf("short unwind accepted: %s", s.stdout)
	}
}

func TestLoopAdjustRefusesAboveMaxLTV(t *testing.T) {
	shares := new(big.Int).Mul(big.NewInt(3_000_000_000), big.NewInt(1_000_000))
	h := loopHarness(t, e18(3), shares, true)
	r := h.run("loop", "adjust", "--chain", "1", "--protocol", "morpho", "--market", loopMarket().ID().Hex(), "--target-leverage", "9", "--json")
	if r.code == 0 || !strings.Contains(r.stdout, "exceeds this market's max") {
		t.Fatalf("leverage above the market max accepted: %s", r.stdout)
	}
}

func TestLoopSubmitRechecksRiskAfterPriceMove(t *testing.T) {
	h, price := loopHarnessPriced(t, new(big.Int), new(big.Int), false)
	d, _ := lending.Morpho(1)
	loopSwapService(t, h, loopLoan, loopColl, d.Bundler3, d.GeneralAdapter1, "1970000000000000000", nil)
	r := h.run("loop", "open", "--chain", "1", "--protocol", "morpho", "--market", loopMarket().ID().Hex(), "--deposit", "1", "--target-leverage", "2", "--json")
	if r.code != 0 {
		t.Fatalf("loop open: %s %s", r.stdout, r.stderr)
	}
	id, dg := r.data()["intent_id"].(string), r.data()["digest"].(string)
	// Collateral halves in price: the sealed 2x loop would now exceed max LTV.
	price.SetInt64(1000)
	s := h.run("loop", "submit", "--intent", id, "--digest", dg, "--json")
	if s.code == 0 || !strings.Contains(s.stdout, "position changed since prepare") {
		t.Fatalf("stale loop submitted after the price moved: %s %s", s.stdout, s.stderr)
	}
}

// rpcRevertingSimulation forwards every RPC to upstream except
// eth_simulateV1, whose call at index fails with reason.
func rpcRevertingSimulation(t *testing.T, upstream string, index int, reason string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "eth_simulateV1" {
			resp, err := http.Post(upstream, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Errorf("forward: %v", err)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(w, resp.Body)
			return
		}
		sim := req.Params[0].(map[string]any)["blockStateCalls"].([]any)[0].(map[string]any)
		n := len(sim["calls"].([]any))
		out := make([]string, n)
		for i := range out {
			out[i] = `{"status":"0x1","returnData":"0x"}`
			if i == index {
				out[i] = fmt.Sprintf(`{"status":"0x0","returnData":"0x","error":{"code":-32015,"message":"execution reverted: %s"}}`, reason)
			}
		}
		fmt.Fprintf(w, `{"result":[{"calls":[%s]}]}`, strings.Join(out, ","))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A revert in the simulated bundle fails prepare with the reason and journals
// nothing, instead of surfacing at submit.
func TestLoopOpenPrepareFailsOnSimulatedRevert(t *testing.T) {
	h := loopHarness(t, new(big.Int), new(big.Int), false)
	d, _ := lending.Morpho(1)
	loopSwapService(t, h, loopLoan, loopColl, d.Bundler3, d.GeneralAdapter1, "1970000000000000000", nil)
	h.rpcURL = rpcRevertingSimulation(t, h.rpcURL, 1, "WARRANT_REQUIRED_FOR_RECIPIENT")
	id := loopMarket().ID().Hex()
	r := h.run("loop", "open", "--chain", "1", "--protocol", "morpho", "--market", id, "--deposit", "1", "--target-leverage", "2", "--json")
	if r.code == 0 || !strings.Contains(r.stdout, "WARRANT_REQUIRED_FOR_RECIPIENT") || !strings.Contains(r.stdout, "REJECTED") {
		t.Fatalf("simulated revert not reported: %s %s", r.stdout, r.stderr)
	}
}
