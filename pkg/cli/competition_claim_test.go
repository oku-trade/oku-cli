package cli

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

var testProof = "0x" + strings.Repeat("ab", 32)

func merklRewards(agent, distributor string, amount, claimed string) map[string]any {
	return map[string]any{"chains": []any{map[string]any{
		"chain":              map[string]any{"id": "8453", "name": "Base"},
		"distributorAddress": distributor,
		"rewards": []any{
			// Another token the wallet earned: never part of the claim.
			map[string]any{"amount": "999", "claimed": "0", "proofs": []any{testProof}, "recipient": agent, "token": map[string]any{"address": "0x4200000000000000000000000000000000000006", "decimals": 18}},
			map[string]any{"root": "0xroot", "amount": amount, "claimed": claimed, "proofs": []any{testProof, testProof}, "recipient": agent, "token": map[string]any{"address": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", "decimals": 6, "symbol": "USDC"}},
		},
	}}}
}

// baseRPC answers the Base calls one claim needs: no eth_simulateV1, gas,
// fees and a confirmed receipt.
func baseRPC(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_simulateV1":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"the method eth_simulateV1 does not exist"}}`)
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0x2105"}`)
		case "eth_estimateGas":
			fmt.Fprint(w, `{"result":"0x30000"}`)
		case "eth_feeHistory":
			fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1","0x1"]}}`)
		case "eth_maxPriorityFeePerGas", "eth_gasPrice":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_blockNumber":
			fmt.Fprint(w, `{"result":"0x30"}`)
		case "eth_getBlockByNumber":
			fmt.Fprint(w, `{"result":{"hash":"0x`+strings.Repeat("a", 64)+`"}}`)
		case "eth_getBalance":
			fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
		case "eth_getTransactionReceipt":
			fmt.Fprint(w, `{"result":{"status":"0x1","transactionHash":"0x`+strings.Repeat("a", 64)+`","blockHash":"0x`+strings.Repeat("a", 64)+`","blockNumber":"0x10"}}`)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCompetitionClaimPrepareSubmit(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	agent := strings.ToLower(h.fake.agentAddress)
	var asked map[string]any
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method == "GetUserRewards" {
			asked = body
			return merklRewards(agent, merklDistributor, "300000000", "100000000"), true
		}
		return nil, false
	}
	h.rpcChain, h.rpcURL = "8453", baseRPC(t).URL
	prep := h.run("competition", "claim", "prepare", "--json")
	if prep.code != 0 {
		t.Fatalf("prepare: %s %s", prep.stdout, prep.stderr)
	}
	if asked["address"] != agent || asked["fresh"] != true || fmt.Sprint(asked["chainIds"]) != "[8453]" {
		t.Fatalf("GetUserRewards request: %v", asked)
	}
	d := prep.data()
	preview := d["preview"].(map[string]any)
	if preview["claimable_usdc"] != "200" || preview["simulated"] != false {
		t.Fatalf("preview: %v", preview)
	}
	in := d["intent"].(map[string]any)
	id, digest := in["intent_id"].(string), in["digest"].(string)
	tpl := in["templates"].([]any)[0].(map[string]any)
	want, _ := encodeMerklClaim(agent, big.NewInt(300000000), [][32]byte{proofBytes(), proofBytes()})
	if !strings.EqualFold(tpl["to"].(string), merklDistributor) || tpl["data"] != hexData(want) {
		t.Fatalf("claim template: %v", tpl)
	}
	if h.privy.rpcCalls != 0 {
		t.Fatal("prepare contacted the signer")
	}
	if r := h.run("competition", "claim", "submit", "--intent", id, "--digest", "wrong", "--json"); r.errCode() != "PAYLOAD_MISMATCH" || h.privy.rpcCalls != 0 {
		t.Fatalf("wrong digest: %s", r.stdout)
	}
	sub := h.run("competition", "claim", "submit", "--intent", id, "--digest", digest, "--json")
	if sub.code != 0 || sub.data()["intent"].(map[string]any)["state"] != "confirmed" {
		t.Fatalf("submit: %s %s", sub.stdout, sub.stderr)
	}
	if len(h.privy.transactions) != 1 || !strings.Contains(fmt.Sprint(h.privy.transactions[0]), strings.TrimPrefix(hexData(want), "0x")[:40]) {
		t.Fatalf("sent transaction: %v", h.privy.transactions)
	}
	if r := h.run("competition", "claim", "submit", "--intent", id, "--digest", digest, "--json"); r.errCode() != "INTENT_STATE" {
		t.Fatalf("resubmit: %s", r.stdout)
	}
	if r := h.run("competition", "claim", "status", "--intent", id, "--json"); r.code != 0 || r.data()["intent"].(map[string]any)["state"] != "confirmed" {
		t.Fatalf("status: %s", r.stdout)
	}
}

func proofBytes() [32]byte {
	var p [32]byte
	for i := range p {
		p[i] = 0xab
	}
	return p
}

func TestCompetitionClaimRefusals(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	agent := strings.ToLower(h.fake.agentAddress)
	reply := merklRewards(agent, merklDistributor, "100", "100")
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		if method == "GetUserRewards" {
			return reply, true
		}
		return nil, false
	}
	if r := h.run("competition", "claim", "prepare", "--json"); r.errCode() != "NOT_FOUND" {
		t.Fatalf("fully claimed: %s", r.stdout)
	}
	reply = merklRewards(agent, "0x1111111111111111111111111111111111111111", "300", "0")
	if r := h.run("competition", "claim", "prepare", "--json"); r.errCode() != "PAYLOAD_MISMATCH" {
		t.Fatalf("foreign distributor: %s", r.stdout)
	}
	reply = merklRewards("0x2222222222222222222222222222222222222222", merklDistributor, "300", "0")
	if r := h.run("competition", "claim", "prepare", "--json"); r.errCode() != "PAYLOAD_MISMATCH" {
		t.Fatalf("foreign recipient: %s", r.stdout)
	}
	if h.privy.rpcCalls != 0 {
		t.Fatal("refusal contacted the signer")
	}
}

func TestVerifyCompetitionClaimRejectsTampering(t *testing.T) {
	agent := "0x3333333333333333333333333333333333333333"
	good, _ := encodeMerklClaim(agent, big.NewInt(5), [][32]byte{proofBytes()})
	base := func() *journal.OnchainRecord {
		return &journal.OnchainRecord{OnchainIntent: journal.OnchainIntent{Kind: competitionClaimKind, ChainID: 8453, Sender: agent, Recipient: agent, Token: competitionRewardToken, Amount: "5", Templates: []journal.OnchainTemplate{{ChainID: 8453, To: merklDistributor, Data: hexData(good), Value: "0x0"}}}}
	}
	if err := verifyCompetitionClaim(base(), agent); err != nil {
		t.Fatal(err)
	}
	other, _ := encodeMerklClaim("0x4444444444444444444444444444444444444444", big.NewInt(5), [][32]byte{proofBytes()})
	for name, mutate := range map[string]func(*journal.OnchainRecord){
		"target":    func(r *journal.OnchainRecord) { r.Templates[0].To = "0x5555555555555555555555555555555555555555" },
		"value":     func(r *journal.OnchainRecord) { r.Templates[0].Value = "0x1" },
		"user":      func(r *journal.OnchainRecord) { r.Templates[0].Data = hexData(other) },
		"amount":    func(r *journal.OnchainRecord) { r.Amount = "6" },
		"chain":     func(r *journal.OnchainRecord) { r.ChainID = 1 },
		"two calls": func(r *journal.OnchainRecord) { r.Templates = append(r.Templates, r.Templates[0]) },
	} {
		r := base()
		mutate(r)
		if verifyCompetitionClaim(r, agent) == nil {
			t.Errorf("%s tampering accepted", name)
		}
	}
}
