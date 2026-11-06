package lending

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Decoded is one ABI-decoded call.
type Decoded struct {
	To     common.Address
	Method string
	Args   []any
}

// Decode resolves calldata against the lending ABI. Unknown selectors fail.
func Decode(to common.Address, data []byte) (Decoded, error) {
	if len(data) < 4 {
		return Decoded{}, errors.New("calldata too short")
	}
	m, err := morphoABI.MethodById(data[:4])
	prefix := ""
	if err != nil {
		if m, err = loopABI.MethodById(data[:4]); err != nil {
			return Decoded{}, fmt.Errorf("unknown call 0x%x to %s", data[:4], to.Hex())
		}
		prefix = "loop:"
	}
	args, err := m.Inputs.Unpack(data[4:])
	if err != nil {
		return Decoded{}, fmt.Errorf("%s: %w", m.Name, err)
	}
	// Re-encode so trailing bytes or non-canonical encodings are rejected.
	again, err := m.Inputs.Pack(args...)
	if err != nil || !bytes.Equal(again, data[4:]) {
		return Decoded{}, fmt.Errorf("%s: non-canonical calldata", m.Name)
	}
	return Decoded{To: to, Method: prefix + m.Name, Args: args}, nil
}

// EVCItem is one decoded EVC batch item.
type EVCItem struct {
	OnBehalf common.Address
	Value    *big.Int
	Data     []byte
	Call     Decoded
}

// DecodeEVCBatch decodes EVC.batch calldata and every item inside it.
func DecodeEVCBatch(data []byte, evc common.Address) ([]EVCItem, error) {
	d, err := Decode(evc, data)
	if err != nil {
		return nil, err
	}
	if d.Method != "batch" {
		return nil, fmt.Errorf("expected EVC batch, got %s", d.Method)
	}
	raw := abi.ConvertType(d.Args[0], new([]struct {
		TargetContract    common.Address
		OnBehalfOfAccount common.Address
		Value             *big.Int
		Data              []byte
	})).(*[]struct {
		TargetContract    common.Address
		OnBehalfOfAccount common.Address
		Value             *big.Int
		Data              []byte
	})
	out := make([]EVCItem, 0, len(*raw))
	for _, it := range *raw {
		if it.Value.Sign() != 0 {
			return nil, errors.New("EVC batch item carries native value")
		}
		c, err := Decode(it.TargetContract, it.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, EVCItem{OnBehalf: it.OnBehalfOfAccount, Value: it.Value, Data: it.Data, Call: c})
	}
	return out, nil
}

// SubAccount mirrors Euler's sub-account derivation: the owner address with
// its last byte XORed by the index.
func SubAccount(owner common.Address, index uint8) common.Address {
	owner[19] ^= index
	return owner
}
