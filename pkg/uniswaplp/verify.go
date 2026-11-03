package uniswaplp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Template is one stored transaction of a prepared intent.
type Template struct {
	To    string
	Data  string
	Value *big.Int
}

func decodeHex(s string) ([]byte, error) {
	if !strings.HasPrefix(s, "0x") {
		return nil, errors.New("calldata must be 0x hex")
	}
	return hex.DecodeString(s[2:])
}

// VerifyTemplates checks that a stored transaction list is exactly a set of
// approvals this package would build for the plan followed by the plan's own
// action calldata. Anything else (other spenders, other tokens, extra calls,
// edited action bytes) is refused before signing.
func VerifyTemplates(p Plan, d Deployment, ts []Template) error {
	if len(ts) == 0 {
		return errors.New("no transactions")
	}
	if !SameAddress(p.Manager, d.Manager.Hex()) || p.ChainID != d.ChainID || p.Version != d.Version {
		return errors.New("plan position manager differs from the chain deployment")
	}
	want, err := p.Calldata()
	if err != nil {
		return err
	}
	last := ts[len(ts)-1]
	if !SameAddress(last.To, want.To) || !strings.EqualFold(last.Data, want.Data) || last.Value.Cmp(want.Value) != 0 {
		return errors.New("action calldata differs from the approved plan")
	}
	spend, err := p.Spend()
	if err != nil {
		return err
	}
	required := map[common.Address]*big.Int{}
	for i, t := range []Token{p.Token0, p.Token1} {
		amount := spend.Amount0
		if i == 1 {
			amount = spend.Amount1
		}
		if amount.Sign() > 0 && !t.IsNative() && !(p.Version == V3 && p.NativeSide == i) {
			required[common.HexToAddress(t.Address)] = amount
		}
	}
	for _, t := range ts[:len(ts)-1] {
		if t.Value.Sign() != 0 {
			return errors.New("approval carries value")
		}
		data, err := decodeHex(t.Data)
		if err != nil {
			return err
		}
		if err := verifyApproval(d, required, common.HexToAddress(t.To), data); err != nil {
			return err
		}
	}
	return nil
}

func verifyApproval(d Deployment, required map[common.Address]*big.Int, to common.Address, data []byte) error {
	if len(data) < 4 {
		return errors.New("approval calldata too short")
	}
	if d.Version == V4 && to == d.Permit2 {
		m, err := permit2.MethodById(data[:4])
		if err != nil || m.Name != "approve" {
			return errors.New("unexpected Permit2 call")
		}
		args, err := m.Inputs.Unpack(data[4:])
		if err != nil {
			return err
		}
		token, spender, amount := args[0].(common.Address), args[1].(common.Address), args[2].(*big.Int)
		if _, ok := required[token]; !ok || spender != d.Manager || amount.Cmp(maxUint160) != 0 {
			return fmt.Errorf("Permit2 approval for %s is not one the plan needs", token.Hex())
		}
		if !bytes.Equal(data, Permit2Approve(token, spender, amount, args[3].(*big.Int).Uint64())) {
			return errors.New("non-canonical Permit2 approval")
		}
		return nil
	}
	need, ok := required[to]
	if !ok {
		return fmt.Errorf("approval targets %s, which the plan does not deposit", to.Hex())
	}
	m, err := erc20.MethodById(data[:4])
	if err != nil || m.Name != "approve" {
		return errors.New("unexpected token call")
	}
	args, err := m.Inputs.Unpack(data[4:])
	if err != nil {
		return err
	}
	spender, amount := args[0].(common.Address), args[1].(*big.Int)
	if !bytes.Equal(data, ERC20Approve(spender, amount)) {
		return errors.New("non-canonical approval")
	}
	switch d.Version {
	case V3:
		if spender != d.Manager || amount.Cmp(need) != 0 {
			return errors.New("v3 approval must be the exact deposit to the position manager")
		}
	case V4:
		if spender != d.Permit2 || amount.Sign() != 0 && amount.Cmp(maxUint256) != 0 {
			return errors.New("v4 token approval must reset or approve Permit2")
		}
	}
	return nil
}
