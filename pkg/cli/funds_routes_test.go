package cli

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type testWarrant struct {
	Nonce           *big.Int
	ValidBefore     *big.Int
	ValidAfter      *big.Int
	VerifyingSigner common.Address
	Signature       []byte
}

func TestRouteExecutionAuthorization(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(routerABI))
	if err != nil {
		t.Fatal(err)
	}
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	output := common.HexToAddress("0x2222222222222222222222222222222222222222")
	recipient := common.HexToAddress("0x4444444444444444444444444444444444444444")
	target := common.HexToAddress("0x5555555555555555555555555555555555555555")
	warrant := testWarrant{big.NewInt(0), big.NewInt(0), big.NewInt(0), common.Address{}, []byte{}}
	amount := big.NewInt(10000000)
	pack := func(method string, args ...any) string {
		t.Helper()
		data, e := parsed.Pack(method, args...)
		if e != nil {
			t.Fatal(e)
		}
		return fmt.Sprintf("0x%x", data)
	}
	data := pack("fillQuoteTokenToToken", token, output, target, target, []byte{1, 2}, amount, big.NewInt(0), recipient, warrant)
	in := journal.OnchainIntent{Kind: "swap", ChainID: 42161, OutChain: 42161, Token: token.Hex(), OutToken: output.Hex(), Recipient: recipient.Hex(), Target: okuRouter, Spender: okuRouter, Amount: amount.String(), MinOut: "100"}
	approval := func(spender string) *v2.ApprovalPayloadV2 {
		v, e := structpb.NewStruct(map[string]any{"spender": spender, "amount": amount.String()})
		if e != nil {
			t.Fatal(e)
		}
		return &v2.ApprovalPayloadV2{To: token.Hex(), FunctionName: "approve", Values: v}
	}
	tx := &v2.TransactionPayload{ChainId: 42161, To: okuRouter, Value: "0x0", Data: data}
	info := &v2.ExecutionInformation{Trade: tx}
	if templates, e := verifyExecution(in, "100", info, []*v2.ApprovalPayloadV2{approval(okuRouter)}); e != nil || len(templates) != 2 || !strings.EqualFold(templates[0].To, token.Hex()) || templates[0].Data[:10] != "0x095ea7b3" {
		t.Fatalf("verified token swap: %v %+v", e, templates)
	}
	tests := []struct {
		name   string
		mutate func(*journal.OnchainIntent, *v2.TransactionPayload, **v2.ApprovalPayloadV2)
	}{
		{"wrong router", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			tx.To = target.Hex()
		}},
		{"wrong recipient", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			tx.Data = pack("fillQuoteTokenToToken", token, output, target, target, []byte{1}, amount, big.NewInt(0), target, warrant)
		}},
		{"wrong amount", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			tx.Data = pack("fillQuoteTokenToToken", token, output, target, target, []byte{1}, big.NewInt(10000001), big.NewInt(0), recipient, warrant)
		}},
		{"aggregator approval", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			*a = approval(target.Hex())
		}},
		{"permit selector", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			signature := "fillQuoteTokenToTokenWithPermit(address,address,address,address,bytes,uint256,uint256,address,(uint256,uint256,uint256,uint8,uint8,bytes32,bytes32),(uint160,uint48,uint48,address,bytes))"
			tx.Data = fmt.Sprintf("0x%x", crypto.Keccak256([]byte(signature))[:4]) + data[10:]
		}},
		{"chain mismatch", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) { tx.ChainId = 8453 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copyIntent := in
			copyTx := proto.Clone(tx).(*v2.TransactionPayload)
			ap := approval(okuRouter)
			tc.mutate(&copyIntent, copyTx, &ap)
			if _, err := verifyExecution(copyIntent, "100", &v2.ExecutionInformation{Trade: copyTx}, []*v2.ApprovalPayloadV2{ap}); err == nil {
				t.Fatal("unsafe swap accepted")
			}
		})
	}
	eth := in
	eth.Token = "native"
	eth.Spender = ""
	eth.Amount = "10000000"
	ethTx := &v2.TransactionPayload{ChainId: 42161, To: okuRouter, Value: "0x989680", Data: pack("fillQuoteEthToToken", output, target, []byte{1}, big.NewInt(0), recipient, warrant)}
	if templates, err := verifyExecution(eth, "100", &v2.ExecutionInformation{Trade: ethTx}, nil); err != nil || len(templates) != 1 {
		t.Fatalf("ETH swap refused: %v %+v", err, templates)
	}
}

func TestSwapMarketSelectionAndLegacyApproval(t *testing.T) {
	if !acceptablePriceImpact(-0.0039) || acceptablePriceImpact(math.NaN()) || acceptablePriceImpact(0.16) {
		t.Fatal("favourable price impact rejected or unsafe impact accepted")
	}
	markets := supportedMarkets([]*v2.MarketStatus{{Name: "zeroex", Active: true, SupportedChainIds: []int32{42161}}, {Name: "inactive", SupportedChainIds: []int32{42161}}, {Name: "other", Active: true, SupportedChainIds: []int32{8453}}}, 42161)
	if len(markets) != 1 || markets[0] != "zeroex" {
		t.Fatalf("enabled markets for Arbitrum: %v", markets)
	}
	values, err := structpb.NewStruct(map[string]any{"spender": okuRouter, "amount": "1000000"})
	if err != nil {
		t.Fatal(err)
	}
	modern := []*v2.ApprovalPayloadV2{{To: arbitrumUSDC, FunctionName: "approve", Values: values}}
	legacy := []*v2.ApprovalPayload{{ChainId: 42161, Address: arbitrumUSDC, Approvee: okuRouter, Amount: "1000000"}}
	if !consistentSwapApprovals(legacy, modern, 42161, arbitrumUSDC, "1000000") {
		t.Fatal("matching legacy and v2 approval rejected")
	}
	legacy[0].Approvee = "0x1111111111111111111111111111111111111111"
	if consistentSwapApprovals(legacy, modern, 42161, arbitrumUSDC, "1000000") {
		t.Fatal("divergent legacy spender accepted")
	}
}

// Routers come from the shared chain-config package, not a local table:
// chains with their own deployment resolve to it, and deprecated or
// router-less chains (zkSync, Blast) are refused rather than defaulted.
func TestRouterForUsesChainConfig(t *testing.T) {
	for chain, want := range map[uint64]string{
		42161:  okuRouter,
		5000:   "0x86d61277970685075a0BF66023c219dc4A6e3F6d", // Mantle
		1329:   "0x7B060A98BA242Ae42D6027a60937787eBe33DEBe", // Sei
		30:     "0x0906896E8564c61F40778E2B7A46E1269Aaa681e", // Rootstock
		324:    "",                                           // zkSync: deprecated, no router
		81457:  "",                                           // Blast: deprecated
		1:      okuRouter,
		999999: "",
	} {
		if got := routerFor(chain); !strings.EqualFold(got, want) {
			t.Fatalf("chain %d: router %q, want %q", chain, got, want)
		}
	}
}
