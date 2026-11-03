package cli

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/uniswaplp"
)

// lpFakeABI is the subset of view functions the fake chain answers.
const lpFakeABI = `[
{"name":"aggregate3","type":"function","inputs":[{"name":"calls","type":"tuple[]","components":[{"name":"target","type":"address"},{"name":"allowFailure","type":"bool"},{"name":"callData","type":"bytes"}]}],"outputs":[{"name":"returnData","type":"tuple[]","components":[{"name":"success","type":"bool"},{"name":"returnData","type":"bytes"}]}]},
{"name":"positions","type":"function","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[{"name":"nonce","type":"uint96"},{"name":"operator","type":"address"},{"name":"token0","type":"address"},{"name":"token1","type":"address"},{"name":"fee","type":"uint24"},{"name":"tickLower","type":"int24"},{"name":"tickUpper","type":"int24"},{"name":"liquidity","type":"uint128"},{"name":"f0","type":"uint256"},{"name":"f1","type":"uint256"},{"name":"o0","type":"uint128"},{"name":"o1","type":"uint128"}]},
{"name":"ownerOf","type":"function","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[{"name":"","type":"address"}]},
{"name":"getPool","type":"function","inputs":[{"name":"","type":"address"},{"name":"","type":"address"},{"name":"","type":"uint24"}],"outputs":[{"name":"","type":"address"}]},
{"name":"slot0","type":"function","inputs":[],"outputs":[{"name":"sqrtPriceX96","type":"uint160"},{"name":"tick","type":"int24"},{"name":"a","type":"uint16"},{"name":"b","type":"uint16"},{"name":"c","type":"uint16"},{"name":"d","type":"uint8"},{"name":"e","type":"bool"}]},
{"name":"tickSpacing","type":"function","inputs":[],"outputs":[{"name":"","type":"int24"}]},
{"name":"feeGrowthGlobal0X128","type":"function","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
{"name":"feeGrowthGlobal1X128","type":"function","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
{"name":"ticks","type":"function","inputs":[{"name":"tick","type":"int24"}],"outputs":[{"name":"a","type":"uint128"},{"name":"b","type":"int128"},{"name":"f0","type":"uint256"},{"name":"f1","type":"uint256"},{"name":"c","type":"int56"},{"name":"d","type":"uint160"},{"name":"e","type":"uint32"},{"name":"g","type":"bool"}]},
{"name":"decimals","type":"function","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
{"name":"symbol","type":"function","inputs":[],"outputs":[{"name":"","type":"string"}]}
]`

var (
	lpToken0 = common.HexToAddress("0x1000000000000000000000000000000000000001")
	lpToken1 = common.HexToAddress("0x2000000000000000000000000000000000000002")
	lpPoolA  = common.HexToAddress("0x3000000000000000000000000000000000000003")
)

// lpChainRPC fakes one v3 position (token id 7, owned by owner) in a 0.3%
// pool at tick -200010 on chain 1. It records every eth_estimateGas call.
func lpChainRPC(t *testing.T, owner string, estimates *[]map[string]any) string {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(lpFakeABI))
	if err != nil {
		t.Fatal(err)
	}
	sqrt, err := uniswaplp.SqrtRatioAtTick(-200010)
	if err != nil {
		t.Fatal(err)
	}
	zero := new(big.Int)
	answer := func(data []byte) []byte {
		m, err := parsed.MethodById(data[:4])
		if err != nil {
			t.Errorf("unexpected selector %x", data[:4])
			return nil
		}
		var out []any
		switch m.Name {
		case "positions":
			out = []any{zero, common.Address{}, lpToken0, lpToken1, big.NewInt(3000), big.NewInt(-201000), big.NewInt(-199020), big.NewInt(940103370731410), zero, zero, big.NewInt(5), zero}
		case "ownerOf":
			out = []any{common.HexToAddress(owner)}
		case "getPool":
			out = []any{lpPoolA}
		case "slot0":
			out = []any{sqrt, big.NewInt(-200010), uint16(0), uint16(0), uint16(0), uint8(0), true}
		case "tickSpacing":
			out = []any{big.NewInt(60)}
		case "feeGrowthGlobal0X128", "feeGrowthGlobal1X128":
			out = []any{zero}
		case "ticks":
			out = []any{zero, zero, zero, zero, big.NewInt(0), zero, uint32(0), true}
		case "decimals":
			out = []any{uint8(18)}
		case "symbol":
			out = []any{"TKN"}
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
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_gasPrice", "eth_maxPriorityFeePerGas":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_feeHistory":
			fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1"]}}`)
		case "eth_estimateGas":
			call, _ := req.Params[0].(map[string]any)
			*estimates = append(*estimates, call)
			fmt.Fprint(w, `{"result":"0x30000"}`)
		case "eth_call":
			call, _ := req.Params[0].(map[string]any)
			data, _ := hex.DecodeString(strings.TrimPrefix(call["data"].(string), "0x"))
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
				rows[i] = row{true, answer(c.CallData)}
			}
			b, err := parsed.Methods["aggregate3"].Outputs.Pack(rows)
			if err != nil {
				t.Errorf("pack aggregate3: %v", err)
			}
			fmt.Fprintf(w, `{"result":"0x%x"}`, b)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestLPClosePrepareBindsVerifiedCalldata(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	var estimates []map[string]any
	h.rpcChain, h.rpcURL = "1", lpChainRPC(t, h.fake.agentAddress, &estimates)

	pos := h.run("lp", "position", "--chain", "1", "--token-id", "7", "--json")
	if pos.code != 0 {
		t.Fatalf("lp position: %s %s", pos.stdout, pos.stderr)
	}

	r := h.run("lp", "close", "prepare", "--chain", "1", "--token-id", "7", "--json")
	if r.code != 0 {
		t.Fatalf("lp close prepare: %s %s", r.stdout, r.stderr)
	}
	d := r.data()
	preview := d["preview"].(map[string]any)
	if d["state"] != "prepared" || preview["action"] != "close" || preview["approvals"].(float64) != 0 || preview["simulated"] != true {
		t.Fatalf("unexpected close intent: %s", r.stdout)
	}
	// The simulated call is exactly the stored template sent to the v3 manager.
	tpl := d["templates"].([]any)[0].(map[string]any)
	if len(estimates) != 1 || estimates[0]["data"] != tpl["data"] || !strings.EqualFold(estimates[0]["to"].(string), tpl["to"].(string)) {
		t.Fatalf("simulation differs from the stored call: %v vs %v", estimates, tpl)
	}
	// Removals bound by the SDK burn minimums for this position.
	if preview["limit0"] != "0.948425436948174185" || preview["limit1"] != "0.00000000195431831" {
		t.Fatalf("burn minimums %v/%v", preview["limit0"], preview["limit1"])
	}

	if s := h.run("lp", "submit", "--intent", d["intent_id"].(string), "--digest", "0xbad", "--json"); s.code == 0 || s.errCode() != "PAYLOAD_MISMATCH" {
		t.Fatalf("wrong digest accepted: %s", s.stdout)
	}
	if s := h.run("lp", "status", "--intent", d["intent_id"].(string), "--json"); s.code != 0 || s.data()["state"] != "prepared" {
		t.Fatalf("lp status: %s %s", s.stdout, s.stderr)
	}
}

func TestLPPrepareRefusesForeignPosition(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	var estimates []map[string]any
	h.rpcChain, h.rpcURL = "1", lpChainRPC(t, "0x00000000000000000000000000000000000000bb", &estimates)
	r := h.run("lp", "decrease", "prepare", "--chain", "1", "--token-id", "7", "--percent", "50", "--json")
	if r.code == 0 || !strings.Contains(r.stdout, "not owned by the agent") || len(estimates) != 0 {
		t.Fatalf("foreign position accepted: %s", r.stdout)
	}
}
