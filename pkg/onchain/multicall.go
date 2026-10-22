package onchain

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const multicall3 = "0xcA11bde05977b3631167028862bE2a173976CA11"
const aggregateABI = `[{"name":"aggregate3","type":"function","stateMutability":"view","inputs":[{"name":"calls","type":"tuple[]","components":[{"name":"target","type":"address"},{"name":"allowFailure","type":"bool"},{"name":"callData","type":"bytes"}]}],"outputs":[{"name":"returnData","type":"tuple[]","components":[{"name":"success","type":"bool"},{"name":"returnData","type":"bytes"}]}]}]`

type multicallInput struct {
	Target       common.Address
	AllowFailure bool
	CallData     []byte
}
type multicallOutput struct {
	Success    bool
	ReturnData []byte
}
type TokenMetadata struct {
	Address  string
	Symbol   string
	Decimals int
	Balance  *big.Int
}

func (r HTTPRPC) TokenDetails(ctx context.Context, chain uint64, wallet string, tokens []string) ([]TokenMetadata, error) {
	if len(tokens) == 0 {
		return []TokenMetadata{}, nil
	}
	if !ValidAddress(wallet) {
		return nil, errors.New("invalid wallet address")
	}
	contract, err := abi.JSON(strings.NewReader(aggregateABI))
	if err != nil {
		return nil, err
	}
	calls := make([]multicallInput, 0, len(tokens)*3)
	for _, token := range tokens {
		if !ValidAddress(token) {
			return nil, fmt.Errorf("invalid token address %q", token)
		}
		target := common.HexToAddress(token)
		balance, _ := hex.DecodeString("70a08231" + strings.Repeat("0", 24) + strings.ToLower(wallet[2:]))
		calls = append(calls, multicallInput{target, true, balance}, multicallInput{target, true, []byte{0x95, 0xd8, 0x9b, 0x41}}, multicallInput{target, true, []byte{0x31, 0x3c, 0xe5, 0x67}})
	}
	payload, err := contract.Pack("aggregate3", calls)
	if err != nil {
		return nil, err
	}
	var encoded string
	if err = r.call(ctx, chain, "eth_call", []any{map[string]string{"to": multicall3, "data": "0x" + hex.EncodeToString(payload)}, "latest"}, &encoded); err != nil {
		return nil, err
	}
	result, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil {
		return nil, err
	}
	values, err := contract.Unpack("aggregate3", result)
	if err != nil {
		return nil, err
	}
	rows := abi.ConvertType(values[0], new([]multicallOutput)).(*[]multicallOutput)
	if len(*rows) != len(calls) {
		return nil, fmt.Errorf("multicall returned %d values for %d calls", len(*rows), len(calls))
	}
	details := make([]TokenMetadata, 0, len(tokens))
	for i, token := range tokens {
		bal, sym, dec := (*rows)[i*3], (*rows)[i*3+1], (*rows)[i*3+2]
		if !bal.Success || len(bal.ReturnData) != 32 {
			return nil, fmt.Errorf("balanceOf failed for %s", token)
		}
		if new(big.Int).SetBytes(bal.ReturnData).Sign() == 0 {
			continue
		}
		if !dec.Success || len(dec.ReturnData) != 32 {
			return nil, fmt.Errorf("decimals failed for %s", token)
		}
		n := new(big.Int).SetBytes(dec.ReturnData)
		if !n.IsInt64() || n.Int64() > 36 {
			return nil, fmt.Errorf("invalid decimals for %s", token)
		}
		symbol := ""
		if sym.Success {
			data := sym.ReturnData
			if len(data) == 32 {
				symbol = strings.TrimRight(string(data), "\x00")
			} else if len(data) >= 64 {
				offset := new(big.Int).SetBytes(data[:32])
				if offset.IsInt64() && offset.Int64() >= 0 && offset.Int64()+32 <= int64(len(data)) {
					at := int(offset.Int64())
					length := new(big.Int).SetBytes(data[at : at+32])
					if length.IsInt64() && length.Int64() >= 0 && length.Int64() <= 128 && at+32+int(length.Int64()) <= len(data) {
						symbol = string(data[at+32 : at+32+int(length.Int64())])
					}
				}
			}
		}
		if !utf8.ValidString(symbol) {
			symbol = ""
		}
		details = append(details, TokenMetadata{token, symbol, int(n.Int64()), new(big.Int).SetBytes(bal.ReturnData)})
	}
	return details, nil
}

func (r HTTPRPC) TokenBalances(ctx context.Context, chain uint64, wallet string, tokens []string) ([]*big.Int, error) {
	rows, err := r.TokenDetails(ctx, chain, wallet, tokens)
	if err != nil {
		return nil, err
	}
	out := make([]*big.Int, len(tokens))
	for i, t := range tokens {
		out[i] = big.NewInt(0)
		for _, row := range rows {
			if strings.EqualFold(t, row.Address) {
				out[i] = row.Balance
				break
			}
		}
	}
	return out, nil
}
