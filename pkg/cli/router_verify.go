package cli

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
)

// Derived from BaseAggregator.sol and CanoeHelper.Warrant. Permit variants are intentionally absent.
const routerABI = `[{"type":"function","name":"fillQuoteEthToToken","inputs":[{"name":"buyTokenAddress","type":"address"},{"name":"target","type":"address"},{"name":"swapCallData","type":"bytes"},{"name":"feeAmount","type":"uint256"},{"name":"recipient","type":"address"},{"name":"warrant","type":"tuple","components":[{"name":"nonce","type":"uint160"},{"name":"validBefore","type":"uint48"},{"name":"validAfter","type":"uint48"},{"name":"verifyingSigner","type":"address"},{"name":"signature","type":"bytes"}]}]},{"type":"function","name":"fillQuoteTokenToToken","inputs":[{"name":"sellTokenAddress","type":"address"},{"name":"buyTokenAddress","type":"address"},{"name":"target","type":"address"},{"name":"approvalTarget","type":"address"},{"name":"swapCallData","type":"bytes"},{"name":"sellAmount","type":"uint256"},{"name":"feeAmount","type":"uint256"},{"name":"recipient","type":"address"},{"name":"warrant","type":"tuple","components":[{"name":"nonce","type":"uint160"},{"name":"validBefore","type":"uint48"},{"name":"validAfter","type":"uint48"},{"name":"verifyingSigner","type":"address"},{"name":"signature","type":"bytes"}]}]},{"type":"function","name":"fillQuoteTokenToEth","inputs":[{"name":"sellTokenAddress","type":"address"},{"name":"target","type":"address"},{"name":"approvalTarget","type":"address"},{"name":"swapCallData","type":"bytes"},{"name":"sellAmount","type":"uint256"},{"name":"feePercentageBasisPoints","type":"uint256"},{"name":"recipient","type":"address"},{"name":"warrant","type":"tuple","components":[{"name":"nonce","type":"uint160"},{"name":"validBefore","type":"uint48"},{"name":"validAfter","type":"uint48"},{"name":"verifyingSigner","type":"address"},{"name":"signature","type":"bytes"}]}]}]`

const okuRouter = "0xb1f3a7B816B0681188F54dFa400991B93ADf00ed"

func verifyExecution(in journal.OnchainIntent, quoteMin string, info *v2.ExecutionInformation, approvals []*v2.ApprovalPayloadV2) ([]journal.OnchainTemplate, error) {
	fail := func(reason string) ([]journal.OnchainTemplate, error) {
		return nil, fmt.Errorf("execution does not match prepared intent: %s", reason)
	}
	if in.Kind != "swap" || !onchain.Allowed(in.ChainID) || in.OutChain != in.ChainID || !safeInfo(info) || !onchain.ValidAddress(in.Recipient) || !strings.EqualFold(in.Target, okuRouter) || !strings.EqualFold(info.Trade.To, okuRouter) || info.Trade.ChainId != int64(in.ChainID) {
		return fail("router, chain or route differs")
	}
	min, e := positiveRaw(in.MinOut)
	if e != nil {
		return fail("minimum output invalid")
	}
	quoted, e := positiveRaw(quoteMin)
	if e != nil || quoted.Cmp(min) < 0 {
		return fail("JIT quote below minimum")
	}
	amount, e := positiveRaw(in.Amount)
	if e != nil {
		return fail("invalid input")
	}
	parsed, e := abi.JSON(strings.NewReader(routerABI))
	if e != nil {
		return fail("invalid router ABI")
	}
	data, e := hex.DecodeString(strings.TrimPrefix(info.Trade.Data, "0x"))
	if e != nil || len(data) < 4 {
		return fail("invalid calldata")
	}
	method, e := parsed.MethodById(data[:4])
	if e != nil {
		return fail("unsupported selector (including permit)")
	}
	args, e := method.Inputs.Unpack(data[4:])
	if e != nil {
		return fail("invalid ABI parameters")
	}
	addr := func(v any) string {
		a, ok := v.(common.Address)
		if !ok {
			return ""
		}
		return a.Hex()
	}
	num := func(v any) *big.Int { n, _ := v.(*big.Int); return n }
	match := func(a, b string) bool { return strings.EqualFold(a, b) }
	nativeIn := strings.EqualFold(in.Token, "native")
	nativeOut := strings.EqualFold(in.OutToken, "native")
	var recipient string
	switch method.Name {
	case "fillQuoteEthToToken":
		if !nativeIn || nativeOut || !match(addr(args[0]), in.OutToken) {
			return fail("ETH input or output differs")
		}
		recipient = addr(args[4])
	case "fillQuoteTokenToToken":
		if nativeIn || nativeOut || !match(addr(args[0]), in.Token) || !match(addr(args[1]), in.OutToken) || num(args[5]) == nil || num(args[5]).Cmp(amount) != 0 {
			return fail("tokens or sell amount differ")
		}
		recipient = addr(args[7])
	case "fillQuoteTokenToEth":
		if nativeIn || !nativeOut || !match(addr(args[0]), in.Token) || num(args[4]) == nil || num(args[4]).Cmp(amount) != 0 {
			return fail("token or sell amount differs")
		}
		recipient = addr(args[6])
	default:
		return fail("unsupported router method")
	}
	if !match(recipient, in.Recipient) {
		return fail("recipient differs")
	}
	value, e := onchain.Quantity(info.Trade.Value)
	if e != nil || (nativeIn && value.Cmp(amount) != 0) || (!nativeIn && value.Sign() != 0) {
		return fail("native value differs")
	}
	if len(info.Transactions) > 0 {
		tx := info.Transactions[0]
		if tx == nil || tx.ChainId != info.Trade.ChainId || !match(tx.To, info.Trade.To) || tx.Data != info.Trade.Data || tx.Value != info.Trade.Value {
			return fail("extra or divergent transaction")
		}
	}
	templates := []journal.OnchainTemplate{}
	if nativeIn {
		if len(approvals) != 0 || in.Spender != "" {
			return fail("ETH must not have approval")
		}
	} else {
		if !match(in.Spender, okuRouter) || len(approvals) > 1 {
			return fail("approval spender differs")
		}
		if len(approvals) == 1 {
			ap := approvals[0]
			if ap == nil || !match(ap.To, in.Token) || ap.FunctionName != "approve" || ap.Values == nil {
				return fail("invalid approval")
			}
			values := ap.Values.AsMap()
			spender, _ := values["spender"].(string)
			if !match(spender, okuRouter) || fmt.Sprint(values["amount"]) != in.Amount {
				return fail("approval amount or spender differs")
			}
			approvalData := "0x095ea7b3" + strings.Repeat("0", 24) + strings.ToLower(okuRouter[2:]) + fmt.Sprintf("%064x", amount)
			templates = append(templates, journal.OnchainTemplate{ChainID: in.ChainID, To: in.Token, Value: "0x0", Data: approvalData, Token: in.Token, Amount: onchain.Hex(amount)})
		}
	}
	trade := journal.OnchainTemplate{ChainID: in.ChainID, To: okuRouter, Data: info.Trade.Data, Value: onchain.Hex(value)}
	if !nativeIn {
		trade.Token = in.Token
		trade.Amount = onchain.Hex(amount)
		trade.Spender = okuRouter
	}
	templates = append(templates, trade)
	return templates, nil
}
