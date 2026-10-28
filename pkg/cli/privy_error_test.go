package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/privy"
)

type diagnosticRPC struct{}

func (diagnosticRPC) Balance(context.Context, uint64, string) (*big.Int, error) {
	return big.NewInt(1 << 40), nil
}
func (diagnosticRPC) GasPrice(context.Context, uint64) (*big.Int, error) {
	return big.NewInt(1), nil
}
func (diagnosticRPC) Call(context.Context, uint64, string, string) (*big.Int, error) {
	return big.NewInt(1 << 40), nil
}
func (diagnosticRPC) Receipt(context.Context, uint64, string) (*bool, error) {
	return nil, errors.New("receipt must be resolved separately")
}

type diagnosticSender struct {
	calls int
	err   error
}

func (s *diagnosticSender) SendTransaction(context.Context, string, privy.Transaction) (string, error) {
	s.calls++
	return "", s.err
}

func TestUnknownOutcomeTakesPrecedenceOverNestedSentinels(t *testing.T) {
	for _, cause := range []error{
		&privy.Error{Status: http.StatusUnauthorized, Code: "Unauthorized"},
		context.DeadlineExceeded,
		context.Canceled,
	} {
		err := fmt.Errorf("%w: %w", onchain.ErrUnknown, cause)
		result := output.As(executionError(err, "swap", 42161, "native"))
		if result.Code != output.CodeOutcomeUnknown || result.Code.ExitCode() != 6 || result.Retryable || !errors.Is(result, onchain.ErrUnknown) || !errors.Is(result, cause) {
			t.Fatalf("nested %v changed ambiguous outcome: %+v", cause, result)
		}
	}
}

// No enrollment or native credential store is needed. All HTTP calls and the
// journal are disposable fixtures, including the transaction mined despite 400.
func TestMinedTransactionAfterPrivy400RemainsUnknownUntilReconciled(t *testing.T) {
	t.Setenv("OKU_STATE_DIR", t.TempDir())
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
	const agent = "0x1111111111111111111111111111111111111111"
	const target = "0x2222222222222222222222222222222222222222"
	hash := "0x" + strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/oauth/") {
			w.Header().Set("x-request-id", "secret-reflected-request-id")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"Missing or invalid parameters. Details: nonce too low: address secret-reflected-address, tx: 12 state: 13","code":"transaction_broadcast_failure"}`)
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_getTransactionByHash":
			fmt.Fprintf(w, `{"result":{"hash":%q,"from":%q,"to":%q,"value":"0x1","input":"0x"}}`, hash, agent, target)
		case "eth_blockNumber":
			fmt.Fprint(w, `{"result":"0x80"}`)
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"result":{"hash":%q}}`, hash)
		case "eth_getTransactionReceipt":
			fmt.Fprintf(w, `{"result":{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x10"}}`, hash, hash)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	defer server.Close()
	// Exercise the shared non-2xx response parser without credentials or signing.
	_, upstreamErr := (&privy.Client{AuthURL: server.URL}).StartDeviceAuthorization(context.Background())
	sender := &diagnosticSender{err: upstreamErr}
	j, err := journal.Open("privy-diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	in := journal.OnchainIntent{ID: "mined-after-400", Kind: "send", ChainID: 42161, Sender: agent, Recipient: target, Token: "native", Amount: "1", Templates: []journal.OnchainTemplate{{ChainID: 42161, To: target, Value: "0x1", Data: "0x"}}}
	if _, err = j.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(in.ID, in.Digest(), agent); err != nil {
		t.Fatal(err)
	}
	err = (onchain.Executor{RPC: diagnosticRPC{}, Sender: sender, Journal: j}).Execute(context.Background(), in)
	result := output.As(executionError(err, "send", in.ChainID, in.Token))
	if result.Code != output.CodeOutcomeUnknown || result.Code.ExitCode() != 6 || result.Retryable {
		t.Fatalf("unsafe CLI outcome: %+v", result)
	}
	upstream, ok := result.Details["upstream"].(map[string]any)
	if !ok || upstream["provider"] != "privy" || upstream["status"] != 400 || upstream["code"] != "transaction_broadcast_failure" || !strings.Contains(upstream["cause"].(string), "nonce too low") {
		t.Fatalf("lost structured diagnostic: %+v", result)
	}
	wire, err := json.Marshal(result)
	if err != nil || strings.Contains(string(wire), "secret-reflected") {
		t.Fatalf("unsafe JSON diagnostic: %s (%v)", wire, err)
	}
	if err = j.BeginOnchain(in.ID, in.Digest(), agent); !errors.Is(err, journal.ErrConflict) {
		t.Fatalf("same intent was replayable: %v", err)
	}
	replacement := in
	replacement.ID = "replacement"
	if _, err = j.CreateOnchain(replacement); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(replacement.ID, replacement.Digest(), agent); !errors.Is(err, journal.ErrUnresolved) {
		t.Fatalf("unknown did not block replacement: %v", err)
	}
	a := &App{Getenv: func(key string) string {
		if strings.HasPrefix(key, "OKU_RPC_URL_") {
			return server.URL
		}
		return ""
	}}
	e := &env{app: a, ctx: context.Background(), g: globals{timeout: time.Second}}
	record, err := j.GetOnchain(in.ID)
	if err != nil || record.State != "unknown" || len(record.Hashes) != 0 {
		t.Fatalf("premature terminal outcome: %+v (%v)", record, err)
	}
	// These are the verification, attachment, and confirmed-receipt steps used
	// by reconcile. A provider code or consumed nonce is not a substitute.
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	if err = rpc.VerifyTransaction(context.Background(), in.ChainID, hash, agent, in.Templates[0]); err != nil {
		t.Fatal(err)
	}
	if err = j.AdvanceOnchain(in.ID, "unknown", "sent", hash); err != nil {
		t.Fatal(err)
	}
	record, err = j.GetOnchain(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err = a.resolveReceipt(e, j, record)
	if err != nil || record.State != "confirmed" || len(record.Hashes) != 1 || record.Hashes[0] != hash || sender.calls != 1 {
		t.Fatalf("reconciliation=%+v err=%v sends=%d", record, err, sender.calls)
	}
}
