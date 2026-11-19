//go:build linux

package cli

import (
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/mcp"
	"github.com/oku-trade/oku-cli/pkg/merkl"
)

func participantWithUSDCReward(state string) map[string]any {
	return map[string]any{
		"registered": true,
		"claim":      map[string]any{"chain": "eip155:8453", "distributor": merkl.Distributor},
		"rewards": []any{map[string]any{
			"state": state, "amount": "12.34",
			"token": map[string]any{"chain": "eip155:8453", "address": merkl.BaseUSDC, "decimals": 6},
		}},
	}
}

func TestCompetitionStatusExplicitWalletSkipsPromptingKeyring(t *testing.T) {
	h, keyring := keyringHarness(t)
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.Profiles["main"].CredentialStore = string(auth.StoreKeyring)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	keyring.Unavailable = true
	keyring.BeforeGet = func() { t.Error("the explicit wallet lookup read the prompting credential store") }

	agent := strings.ToLower(h.fake.agentAddress)
	other := "0x2222222222222222222222222222222222222222"
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method != "GetParticipant" {
			return nil, false
		}
		if body["wallet"] == agent {
			return participantWithUSDCReward("COMPETITION_REWARD_STATE_CLAIMABLE"), true
		}
		return participantWithUSDCReward("COMPETITION_REWARD_STATE_CLAIMED"), true
	}
	cs := mcpSession(t, MCPBackend{S: testService(h)}, false)
	for _, wallet := range []string{other, agent} {
		wantNote := wallet == agent
		args := []string{"competition", "status", "--wallet", wallet}
		if r := h.run(args...); r.code != 0 || !strings.Contains(r.stdout, "12.34") || strings.Contains(r.stdout, "Claim with:") || strings.Contains(r.stdout, "may include other campaigns") != wantNote {
			t.Fatalf("human status for %s: %s %s", wallet, r.stdout, r.stderr)
		}
		r := h.run(append(args, "--json")...)
		ok, envelope := callTool(t, cs, mcp.ToolName("competition status"), map[string]any{"wallet": wallet})
		mcpData, _ := envelope["data"].(map[string]any)
		if r.code != 0 || !ok {
			t.Fatalf("status for %s: CLI %s, MCP %v", wallet, r.stdout, envelope)
		}
		for _, data := range []map[string]any{r.data(), mcpData} {
			rewards, _ := data["rewards"].([]any)
			if data["wallet"] != wallet || len(rewards) != 1 || data["next"] != nil || (data["claimNote"] != nil) != wantNote {
				t.Fatalf("status for %s: %v", wallet, data)
			}
		}
	}
	if r := h.run("competition", "status", "--wallet=", "--json"); r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("explicit empty wallet must not fall back to enrollment: %s", r.stdout)
	}
}

func TestCompetitionStatusVerifiesEnrollmentAfterParticipantFetch(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	agent := strings.ToLower(h.fake.agentAddress)
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method != "GetParticipant" || body["wallet"] != agent {
			return nil, false
		}
		// The real credential loader can only verify the binding after this request.
		md := auth.Metadata{Profile: "main", Network: "mainnet", APIURL: h.srv.URL, AccountAddress: acct, AgentAddress: agent, PrivyWalletID: "fixture-wallet"}
		if err := auth.SavePrivyIn(auth.StoreFile, "main", md, auth.Secrets{}); err != nil {
			t.Errorf("write credential during GetParticipant: %v", err)
		}
		return participantWithUSDCReward("COMPETITION_REWARD_STATE_CLAIMABLE"), true
	}
	want := "oku merkl claim prepare --profile main"
	if r := h.run("competition", "status", "--wallet", agent, "--json"); r.code != 0 || r.data()["next"] != want {
		t.Fatalf("CLI did not verify enrollment after participant fetch: %s %s", r.stdout, r.stderr)
	}
	if err := auth.DeletePrivyIn(auth.StoreFile, "main"); err != nil {
		t.Fatal(err)
	}
	cs := mcpSession(t, MCPBackend{S: testService(h)}, false)
	ok, envelope := callTool(t, cs, mcp.ToolName("competition status"), map[string]any{"wallet": agent})
	data, _ := envelope["data"].(map[string]any)
	if !ok || data["next"] != want {
		t.Fatalf("MCP did not verify enrollment after participant fetch: %v", envelope)
	}
}
