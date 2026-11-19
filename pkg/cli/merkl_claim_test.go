package cli

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/merkl"
)

var testProof = "0x" + strings.Repeat("ab", 32)
var claimTxHash = "0x" + strings.Repeat("a", 64)

func campaignBreakdown(id, amount, claimed string) map[string]any {
	return map[string]any{"campaignId": id, "amount": amount, "claimed": claimed}
}

func merklRewards(agent, distributor string, amount, claimed string, breakdowns ...map[string]any) map[string]any {
	return map[string]any{"chains": []any{map[string]any{
		"chain":              map[string]any{"id": "8453", "name": "Base"},
		"distributorAddress": distributor,
		"rewards": []any{
			// Another token the wallet earned: never part of the claim.
			map[string]any{"amount": "999", "claimed": "0", "proofs": []any{testProof}, "recipient": agent, "token": map[string]any{"address": "0x4200000000000000000000000000000000000006", "decimals": 18}},
			map[string]any{"root": "0xroot", "distributionChainId": "8453", "amount": amount, "claimed": claimed, "breakdowns": breakdowns, "proofs": []any{testProof, testProof}, "recipient": agent, "token": map[string]any{"address": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", "chainId": "8453", "decimals": 6, "symbol": "USDC"}},
		},
	}}}
}

// baseRPC answers the Base calls for preparing, submitting or recovering a claim.
func baseRPC(t *testing.T, simulationReverts bool, recovered *journal.OnchainIntent) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("rpc request: %v", err)
			return
		}
		switch req.Method {
		case "eth_simulateV1":
			if simulationReverts {
				fmt.Fprint(w, `{"result":[{"calls":[{"status":"0x0","error":{"message":"invalid proof"}}]}]}`)
				return
			}
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
			fmt.Fprintf(w, `{"result":{"hash":%q}}`, claimTxHash)
		case "eth_getBalance":
			fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
		case "eth_getTransactionReceipt":
			fmt.Fprintf(w, `{"result":{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x10"}}`, claimTxHash, claimTxHash)
		case "eth_getTransactionByHash":
			if recovered == nil {
				t.Error("unexpected transaction lookup")
				return
			}
			tpl := recovered.Templates[0]
			fmt.Fprintf(w, `{"result":{"hash":%q,"from":%q,"to":%q,"input":%q,"value":"0x0","chainId":"0x2105"}}`, claimTxHash, recovered.Sender, tpl.To, tpl.Data)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMerklClaimPrepareSubmit(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	agent := strings.ToLower(h.fake.agentAddress)
	var asked map[string]any
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "GetUserRewards":
			asked = body
			return merklRewards(agent, merkl.Distributor, "300000000", "100000000",
				campaignBreakdown("first-campaign", "150250000", "50250000"),
				campaignBreakdown("second-campaign", "149750000", "49750000")), true
		case "GetParticipant":
			t.Error("Merkl claim called GetParticipant")
		}
		return nil, false
	}
	h.rpcChain, h.rpcURL = "8453", baseRPC(t, false, nil).URL
	prep := h.run("merkl", "claim", "prepare", "--json")
	if prep.code != 0 {
		t.Fatalf("prepare: %s %s", prep.stdout, prep.stderr)
	}
	if asked["address"] != agent || asked["fresh"] != true || fmt.Sprint(asked["chainIds"]) != "[8453]" {
		t.Fatalf("GetUserRewards request: %v", asked)
	}
	d := requireClaimAmounts(t, prep, "prepared")
	preview := d["preview"].(map[string]any)
	if preview["merkl_unclaimed_usdc"] != "200" || preview["simulated"] != false || preview["claim_scope"] != d["claim_scope"] {
		t.Fatalf("preview: %v", preview)
	}
	in := d["intent"].(map[string]any)
	id, digest := in["intent_id"].(string), in["digest"].(string)
	tpl := in["templates"].([]any)[0].(map[string]any)
	proof, err := hexutil.Decode(testProof)
	if err != nil {
		t.Fatal(err)
	}
	want, err := merkl.EncodeUSDCClaim(agent, big.NewInt(300000000), [][32]byte{[32]byte(proof), [32]byte(proof)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(tpl["to"].(string), merkl.Distributor) || tpl["data"] != hexutil.Encode(want) {
		t.Fatalf("claim template: %v", tpl)
	}
	if h.privy.rpcCalls != 0 {
		t.Fatal("prepare contacted the signer")
	}
	if r := h.run("merkl", "claim", "submit", "--intent", id, "--digest", "wrong", "--json"); r.errCode() != "PAYLOAD_MISMATCH" || h.privy.rpcCalls != 0 {
		t.Fatalf("wrong digest: %s", r.stdout)
	}
	j, err := openJournal(&resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	record, err := j.GetOnchain(id)
	if err != nil {
		t.Fatal(err)
	}
	changed := record.OnchainIntent
	changed.ClaimableAmount = "200000001"
	if changed.Digest() == digest {
		t.Fatal("unclaimed Merkl balance is not bound to the digest")
	}
	tampered := record.OnchainIntent
	tampered.ID = "tampered-claim"
	// Even a matching digest cannot authorize noncanonical calldata.
	tampered.Templates = append([]journal.OnchainTemplate(nil), tampered.Templates...)
	tampered.Templates[0].Data += "00"
	if _, err := j.CreateOnchain(tampered); err != nil {
		t.Fatal(err)
	}
	if r := h.run("merkl", "claim", "submit", "--intent", tampered.ID, "--digest", tampered.Digest(), "--json"); r.errCode() != "PAYLOAD_MISMATCH" || !strings.Contains(r.stdout, "prepare again") || h.privy.rpcCalls != 0 {
		t.Fatalf("tampered submit: %s", r.stdout)
	}
	expired := record.OnchainIntent
	expired.ID, expired.ExpiresAt = "expired-claim", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := j.CreateOnchain(expired); err != nil {
		t.Fatal(err)
	}
	if r := h.run("merkl", "claim", "submit", "--intent", expired.ID, "--digest", expired.Digest(), "--json"); r.errCode() != "INTENT_STATE" || !strings.Contains(r.stdout, "expired") || h.privy.rpcCalls != 0 {
		t.Fatalf("expired submit: %s", r.stdout)
	}
	sub := h.run("merkl", "claim", "submit", "--intent", id, "--digest", digest, "--json")
	requireClaimAmounts(t, sub, "confirmed")
	if len(h.privy.transactions) != 1 {
		t.Fatalf("sent transaction: %v", h.privy.transactions)
	}
	sent := h.privy.transactions[0]
	params, _ := sent["params"].(map[string]any)
	tx, _ := params["transaction"].(map[string]any)
	if sent["caip2"] != "eip155:8453" || !strings.EqualFold(fmt.Sprint(tx["to"]), merkl.Distributor) || tx["data"] != hexutil.Encode(want) || tx["value"] != "0x0" {
		t.Fatal("sent transaction did not match the approved Base claim template")
	}
	if r := h.run("merkl", "claim", "submit", "--intent", id, "--digest", digest, "--json"); r.errCode() != "INTENT_STATE" {
		t.Fatalf("resubmit: %s", r.stdout)
	}
	requireClaimAmounts(t, h.run("merkl", "claim", "status", "--intent", id, "--json"), "confirmed")
	if r := h.run("merkl", "claim", "status", "--intent", id); r.code != 0 || !strings.Contains(r.stdout, d["claim_scope"].(string)) {
		t.Fatalf("human status: %s %s", r.stdout, r.stderr)
	}

	// Model a process that handed off the claim but never received the hash.
	recovered := record.OnchainIntent
	recovered.ID = "recovered-claim"
	if _, err := j.CreateOnchain(recovered); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginOnchain(recovered.ID, recovered.Digest(), agent); err != nil {
		t.Fatal(err)
	}
	if err := j.AdvanceOnchain(recovered.ID, "sending", "unknown", ""); err != nil {
		t.Fatal(err)
	}
	h.rpcURL = baseRPC(t, false, &recovered).URL
	requireClaimAmounts(t, h.run("merkl", "claim", "status", "--intent", recovered.ID, "--json"), "unknown")
	requireClaimAmounts(t, h.run("merkl", "claim", "reconcile", "--intent", recovered.ID, "--tx-hash", claimTxHash, "--json"), "confirmed")
	if len(h.privy.transactions) != 1 {
		t.Fatal("reconcile sent another transaction")
	}
}

func requireClaimAmounts(t *testing.T, r run, state string) map[string]any {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("claim command: %s %s", r.stdout, r.stderr)
	}
	d := r.data()
	in, _ := d["intent"].(map[string]any)
	wantScope := fmt.Sprintf("Claims all unclaimed Merkl USDC on Base for %s: 200 USDC (cumulative 300 USDC). Amounts are from prepare and cover every Merkl campaign.", in["sender"])
	if d["merkl_unclaimed_usdc"] != "200" || d["cumulative_merkl_usdc"] != "300" || in["claimable_amount"] != "200000000" || in["amount"] != "300000000" || in["state"] != state ||
		d["recipient_bound"] != true || in["sender"] != in["recipient"] || d["claim_scope"] != wantScope {
		t.Fatalf("claim result (want %s): %s", state, r.stdout)
	}
	return d
}

func TestMerklClaimRefusals(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	agent := strings.ToLower(h.fake.agentAddress)
	reply := merklRewards(agent, merkl.Distributor, "100", "100")
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		switch method {
		case "GetUserRewards":
			return reply, true
		}
		return nil, false
	}
	if r := h.run("merkl", "claim", "prepare", "--json"); r.errCode() != "NOT_FOUND" {
		t.Fatalf("fully claimed: %s", r.stdout)
	}
	reply = merklRewards(agent, "0x1111111111111111111111111111111111111111", "300", "0")
	if r := h.run("merkl", "claim", "prepare", "--json"); r.errCode() != "PAYLOAD_MISMATCH" {
		t.Fatalf("foreign distributor: %s", r.stdout)
	}
	reply = merklRewards("0x2222222222222222222222222222222222222222", merkl.Distributor, "300", "0")
	if r := h.run("merkl", "claim", "prepare", "--json"); r.errCode() != "PAYLOAD_MISMATCH" {
		t.Fatalf("foreign recipient: %s", r.stdout)
	}
	reply = merklRewards(agent, merkl.Distributor, "300000000", "0")
	h.rpcChain, h.rpcURL = "8453", baseRPC(t, true, nil).URL
	if r := h.run("merkl", "claim", "prepare", "--json"); r.errCode() != "REJECTED" || !strings.Contains(r.stdout, "invalid proof") {
		t.Fatalf("simulation revert: %s", r.stdout)
	}
	chain := reply["chains"].([]any)[0].(map[string]any)
	chain["rewards"].([]any)[1].(map[string]any)["proofs"] = nil
	if r := h.run("merkl", "claim", "prepare", "--json"); r.errCode() != "UPSTREAM_ERROR" || !strings.Contains(r.stdout, "not yet claimable") {
		t.Fatalf("missing Merkl proofs: %s", r.stdout)
	}
	if h.privy.rpcCalls != 0 {
		t.Fatal("refusal contacted the signer")
	}
}
