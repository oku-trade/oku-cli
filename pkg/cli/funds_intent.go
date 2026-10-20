package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/oku-trade/oku-cli/pkg/api"
	v1 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/privy"
	"google.golang.org/protobuf/proto"
	"io"
)

// intentTypedData preserves the backend's domain, message and ordered type fields.
// Structpb numbers are IEEE754: values requiring integer precision must arrive
// as strings (as in the web app's JSON typed-data payload).
func intentTypedData(payload *v2.TypedData) (privy.TypedData, error) {
	if payload == nil || payload.Domain == nil || payload.Message == nil || payload.Types == nil || payload.PrimaryType == "" {
		return privy.TypedData{}, errors.New("incomplete typed-data payload")
	}
	out := privy.TypedData{Domain: payload.Domain.AsMap(), Message: payload.Message.AsMap(), PrimaryType: payload.PrimaryType, Types: map[string][]privy.TypedField{}}
	for name, raw := range payload.Types.AsMap() {
		fields, ok := raw.([]any)
		if !ok {
			return out, fmt.Errorf("typed-data type %s is not an array", name)
		}
		for _, field := range fields {
			m, ok := field.(map[string]any)
			if !ok {
				return out, fmt.Errorf("typed-data field %s is malformed", name)
			}
			n, nok := m["name"].(string)
			t, tok := m["type"].(string)
			if !nok || !tok || n == "" || t == "" {
				return out, fmt.Errorf("typed-data field %s has no name/type", name)
			}
			out.Types[name] = append(out.Types[name], privy.TypedField{Name: n, Type: t})
		}
	}
	if _, ok := out.Types["EIP712Domain"]; !ok {
		for _, field := range []privy.TypedField{{Name: "name", Type: "string"}, {Name: "version", Type: "string"}, {Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"}, {Name: "salt", Type: "bytes32"}} {
			if _, exists := out.Domain[field.Name]; exists {
				out.Types["EIP712Domain"] = append(out.Types["EIP712Domain"], field)
			}
		}
	}
	if len(out.Types[out.PrimaryType]) == 0 {
		return out, errors.New("primary typed-data type absent")
	}
	return out, nil
}

func (a *App) intentRouteStatus(e *env, w *resolvedWallet, record *journal.OnchainRecord) (*result, error) {
	ctx, cancel := e.requestCtx()
	defer cancel()
	client := e.client(w.profile)
	if session, err := e.unlock(w.profile, ""); err == nil {
		session.apply(client)
	}
	var history v1.OrderHistoryResponse
	types := []string{record.Kind}
	if err := client.History(ctx, &v1.OrderHistoryRequest{WalletAddr: w.agent, Types: types, MaxEntries: 1000}, &history); err != nil {
		return nil, apiError(err, "order history")
	}
	orderID := record.BackendOrderID
	var trade *v1.OrderTrade
	for _, entry := range history.History {
		if entry != nil && (entry.OrderId == orderID && orderID != "" || entry.OrderId == record.OrderID || record.State == "unknown" && (entry.Metadata["okuOrderId"] == record.OrderID || entry.Metadata["sourceOrderId"] == record.OrderID)) {
			trade = entry
			break
		}
	}
	fill := destinationFill{Status: "unknown"}
	explorer := ""
	txHash := ""
	if trade != nil {
		fill = historyFill(trade)
		if trade.Status == v1.OrderStatus_ORDER_STATUS_OPEN || trade.Status == v1.OrderStatus_ORDER_STATUS_PENDING {
			fill.Status = "pending"
		}
		txHash = trade.TxHash
		if fill.FillTx == "" {
			fill.FillTx = trade.TxHash
		}
		explorer = trade.Metadata["explorerUrl"]
		if orderID == "" {
			orderID = trade.OrderId
		}
		if record.State == "unknown" && record.BackendOrderID == "" && orderID != "" {
			j, err := openJournal(w.profile)
			if err == nil {
				if j.RelayOnchain(record.ID, "unknown", orderID) == nil {
					record, _ = j.GetOnchain(record.ID)
				}
				j.Close()
			}
		}
	}
	if fill.Status == "unknown" && record.State == "submitted" {
		fill.Status = "pending"
	}
	return &result{data: map[string]any{"source": record, "intent_order_id": orderID, "destination_fill": fill, "status": fill.Status, "tx_hash": txHash, "dst_tx_hash": fill.FillTx, "explorer_url": explorer}, human: func(out io.Writer) {
		fmt.Fprintf(out, "%s intent %s: %s; order: %s", record.Kind, record.ID, fill.Status, orderID)
		if explorer != "" {
			fmt.Fprintf(out, "; %s", explorer)
		}
		fmt.Fprintln(out)
	}}, nil
}

func signIntent(ctx context.Context, session *privy.Session, wallet string, req *v2.SigningRequest) (*v2.SigningRequest, error) {
	if len(req.GetTypedData()) == 0 || req.TypedData[0] == nil || req.TypedData[0].Payload == nil {
		return nil, errors.New("missing intent typed-data")
	}
	signed := proto.Clone(req).(*v2.SigningRequest)
	first := signed.TypedData[0]
	if first.Signature == "" {
		typed, err := intentTypedData(first.Payload)
		if err != nil {
			return nil, err
		}
		sig, err := session.SignTypedData(ctx, wallet, typed)
		if err != nil {
			return nil, err
		}
		first.Signature = "0x" + hex.EncodeToString(sig[:])
	}
	return signed, nil
}

func approvalInteger(v any) (*big.Int, error) {
	var text string
	switch n := v.(type) {
	case string:
		text = n
	case float64:
		if n < 0 || n > 9007199254740991 || n != float64(int64(n)) {
			return nil, errors.New("approval numeric value loses precision")
		}
		text = fmt.Sprintf("%.0f", n)
	default:
		return nil, errors.New("invalid approval numeric value")
	}
	var out *big.Int
	if strings.HasPrefix(text, "0x") {
		out, _ = new(big.Int).SetString(text[2:], 16)
	} else {
		out, _ = new(big.Int).SetString(text, 10)
	}
	if out == nil || out.Sign() < 0 || out.BitLen() > 256 {
		return nil, errors.New("invalid approval integer")
	}
	return out, nil
}

// intentApprovals encodes only the backend's approval calls, never an intent trade.
// The Permit2 approve overload is approve(address,address,uint160,uint48).
func intentApprovals(ctx context.Context, rpc onchain.HTTPRPC, chain uint64, owner string, approvals []*v2.ApprovalPayloadV2) ([]journal.OnchainTemplate, error) {
	var templates []journal.OnchainTemplate
	word := func(n *big.Int) string { return fmt.Sprintf("%064x", n) }
	addressWord := func(s string) string { return strings.Repeat("0", 24) + strings.ToLower(s[2:]) }
	for _, ap := range approvals {
		if ap == nil || ap.Values == nil || !onchain.ValidAddress(ap.To) || ap.FunctionName != "approve" {
			return nil, errors.New("unsupported intent approval")
		}
		values := ap.Values.AsMap()
		spender, _ := values["spender"].(string)
		if !onchain.ValidAddress(spender) {
			return nil, errors.New("invalid intent approval spender")
		}
		amount, err := approvalInteger(values["amount"])
		if err != nil {
			return nil, err
		}
		var data string
		switch ap.Type {
		case v2.ApprovalType_APPROVAL_TYPE_ERC20:
			if len(ap.Args) > 0 && (len(ap.Args) != 2 || ap.Args[0] != "spender" || ap.Args[1] != "amount") {
				return nil, errors.New("unexpected ERC20 approve arguments")
			}
			allowance, err := rpc.Call(ctx, chain, ap.To, "0xdd62ed3e"+addressWord(owner)+addressWord(spender))
			if err != nil {
				return nil, err
			}
			if allowance.Cmp(amount) >= 0 {
				continue
			}
			data = "0x095ea7b3" + addressWord(spender) + word(amount)
		case v2.ApprovalType_APPROVAL_TYPE_PERMIT2:
			token, _ := values["token"].(string)
			if !onchain.ValidAddress(token) {
				return nil, errors.New("invalid Permit2 approval token")
			}
			expiration, err := approvalInteger(values["expiration"])
			if err != nil {
				return nil, err
			}
			if amount.BitLen() > 160 || expiration.BitLen() > 48 {
				return nil, errors.New("Permit2 approval fields overflow")
			}
			if len(ap.Args) > 0 && (len(ap.Args) != 4 || ap.Args[0] != "token" || ap.Args[1] != "spender" || ap.Args[2] != "amount" || ap.Args[3] != "expiration") {
				return nil, errors.New("unexpected Permit2 approve arguments")
			}
			selector := crypto.Keccak256([]byte("approve(address,address,uint160,uint48)"))[:4]
			data = "0x" + hex.EncodeToString(selector) + addressWord(token) + addressWord(spender) + word(amount) + word(expiration)
		default:
			return nil, errors.New("unknown intent approval type")
		}
		templates = append(templates, journal.OnchainTemplate{ChainID: chain, To: ap.To, Value: "0x0", Data: data})
	}
	return templates, nil
}

func (a *App) submitSignedIntent(e *env, kind string, record *journal.OnchainRecord, j *journal.Journal, client *api.Client, session *privy.Session, wallet string, signing *v2.SigningRequest, templates []journal.OnchainTemplate) (*result, error) {
	ctx, cancel := context.WithTimeout(e.ctx, onchainExecBudget)
	defer cancel()
	signed, err := signIntent(ctx, session, wallet, signing)
	if err != nil {
		return nil, fmt.Errorf("signing backend intent: %w", err)
	}
	digest, err := j.BindOnchainExecution(record.ID, record.Digest, templates)
	if err != nil {
		return nil, fmt.Errorf("binding intent approvals: %w", err)
	}
	if err = j.BeginOnchain(record.ID, digest, record.Sender); err != nil {
		return nil, fmt.Errorf("starting intent: %w", err)
	}
	if len(templates) > 0 {
		executing := record.OnchainIntent
		executing.Templates = templates
		executor := onchain.Executor{Getenv: a.Getenv, RPC: onchain.HTTPRPC{Getenv: a.Getenv}, Sender: session, Journal: j, WalletID: wallet}
		if err = executor.Execute(ctx, executing); err != nil {
			return nil, executionError(err, kind, record.ChainID, record.Token)
		}
	}
	from := "sending"
	if len(templates) > 0 {
		from = "confirmed"
	}
	// Persist uncertainty BEFORE the non-idempotent backend relay. No invocation of
	// submit may replay signing, approvals or GetExecutionInfo from this point.
	if err = j.AdvanceOnchain(record.ID, from, "unknown", ""); err != nil {
		return nil, err
	}
	req := &v2.GetExecutionInfoRequest{OrderId: record.OrderID, QuoteId: record.QuoteID, Request: &v2.ExecutionRequest{SigningRequest: signed}}
	var info v2.GetExecutionInfoResponse
	if kind == "swap" {
		err = client.Swap(ctx, "GetExecutionInfo", req, &info, true)
	} else {
		err = client.Bridge(ctx, "GetExecutionInfo", req, &info, true)
	}
	if err != nil {
		return nil, apiError(err, "GetExecutionInfo (ambiguous intent relay; use status, never resubmit)")
	}
	if info.Info == nil || info.Info.OrderId == "" {
		return nil, errors.New("backend did not return an intent order ID; relay state unknown; use status")
	}
	if err = j.RelayOnchain(record.ID, "unknown", info.Info.OrderId); err != nil {
		return nil, err
	}
	updated, err := j.GetOnchain(record.ID)
	if err != nil {
		return nil, err
	}
	return routeRecord(updated), nil
}
