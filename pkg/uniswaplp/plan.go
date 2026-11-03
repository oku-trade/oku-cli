package uniswaplp

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Action is one LP write.
type Action string

const (
	ActionOpen     Action = "open"
	ActionIncrease Action = "increase"
	ActionDecrease Action = "decrease"
	ActionCollect  Action = "collect"
	ActionClose    Action = "close"
)

// DeadlineSeconds is how long a prepared action stays valid. The web app uses
// 20 minutes for every LP deadline.
const DeadlineSeconds = 20 * 60

// MaxSlippageBps bounds LP slippage like swap slippage.
const MaxSlippageBps = 500

// Token is one side of a pool as the plan uses it.
type Token struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol,omitempty"`
	Decimals int    `json:"decimals"`
}

// IsNative reports the v4 native currency (address zero).
func (t Token) IsNative() bool { return common.HexToAddress(t.Address) == (common.Address{}) }

// Plan is the exact economic request an LP intent approves. It is stored in
// the journal intent, so every field is bound into the intent digest, and the
// action calldata is rebuilt from it at submit and compared byte for byte.
type Plan struct {
	Action      Action  `json:"action"`
	Version     Version `json:"version"`
	ChainID     uint64  `json:"chain_id"`
	Manager     string  `json:"position_manager"`
	Owner       string  `json:"owner"`
	TokenID     string  `json:"token_id,omitempty"`
	Token0      Token   `json:"token0"`
	Token1      Token   `json:"token1"`
	Fee         int     `json:"fee"`
	TickSpacing int     `json:"tick_spacing"`
	Hooks       string  `json:"hooks,omitempty"`
	TickLower   int     `json:"tick_lower"`
	TickUpper   int     `json:"tick_upper"`
	// Pool price observed at prepare.
	SqrtPriceX96 string `json:"sqrt_price_x96"`
	PoolTick     int    `json:"pool_tick"`
	// Liquidity added (open, increase) or removed (decrease, close). Zero for collect.
	Liquidity string `json:"liquidity"`
	// Desired are the expected token amounts in or out at the observed price.
	Amount0 string `json:"amount0"`
	Amount1 string `json:"amount1"`
	// Limit0/Limit1 bound the action: minimums received or deposited (v3 and
	// every removal) or maximums spent (v4 open and increase).
	Limit0    string `json:"limit0"`
	Limit1    string `json:"limit1"`
	LimitKind string `json:"limit_kind"`
	// NativeSide is 0 or 1 when that side is paid in the native token (v3
	// wraps it through msg.value and refundETH; v4 pays native directly).
	// -1 means no native leg.
	NativeSide  int    `json:"native_side"`
	Value       string `json:"value"`
	SlippageBps int    `json:"slippage_bps"`
	Deadline    int64  `json:"deadline"`
}

func parseRaw(name, s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.Sign() < 0 || v.BitLen() > 256 {
		return nil, fmt.Errorf("invalid %s %q", name, s)
	}
	return v, nil
}

func (p Plan) amounts(a, b string) (Amounts, error) {
	x, err := parseRaw("amount0", a)
	if err != nil {
		return Amounts{}, err
	}
	y, err := parseRaw("amount1", b)
	if err != nil {
		return Amounts{}, err
	}
	return Amounts{x, y}, nil
}

func (p Plan) tokenID() (*big.Int, error) {
	v, ok := new(big.Int).SetString(p.TokenID, 10)
	if !ok || v.Sign() < 0 {
		return nil, fmt.Errorf("invalid token id %q", p.TokenID)
	}
	return v, nil
}

// PoolKey returns the v4 pool key of the plan.
func (p Plan) PoolKey() PoolKey {
	return PoolKey{
		Currency0:   common.HexToAddress(p.Token0.Address),
		Currency1:   common.HexToAddress(p.Token1.Address),
		Fee:         big.NewInt(int64(p.Fee)),
		TickSpacing: big.NewInt(int64(p.TickSpacing)),
		Hooks:       common.HexToAddress(p.Hooks),
	}
}

// refundETH is the v3 PeripheryPayments.refundETH() selector, appended by the
// web app when it pays the WETH side with native.
var refundETH = []byte{0x12, 0x21, 0x0e, 0x8a}

// Call is one transaction the agent sends.
type Call struct {
	To    string
	Data  string
	Value *big.Int
}

// Calldata rebuilds the position-manager call the plan approves. It is the
// single source of the action bytes: prepare stores them and submit compares
// them against a fresh rebuild.
func (p Plan) Calldata() (Call, error) {
	if !common.IsHexAddress(p.Manager) || !common.IsHexAddress(p.Owner) {
		return Call{}, errors.New("plan has an invalid manager or owner")
	}
	r, err := NewRange(p.TickLower, p.TickUpper, p.TickSpacing)
	if err != nil {
		return Call{}, err
	}
	liquidity, err := parseRaw("liquidity", p.Liquidity)
	if err != nil {
		return Call{}, err
	}
	desired, err := p.amounts(p.Amount0, p.Amount1)
	if err != nil {
		return Call{}, err
	}
	limit, err := p.amounts(p.Limit0, p.Limit1)
	if err != nil {
		return Call{}, err
	}
	value, err := parseRaw("value", p.Value)
	if err != nil {
		return Call{}, err
	}
	owner := common.HexToAddress(p.Owner)
	deadline := Deadline(p.Deadline)
	wantLimit := "min"
	if p.Version == V4 && (p.Action == ActionOpen || p.Action == ActionIncrease) {
		wantLimit = "max"
	}
	if p.LimitKind != wantLimit {
		return Call{}, fmt.Errorf("limit kind %q, want %q", p.LimitKind, wantLimit)
	}
	if (p.Action == ActionOpen) != (p.TokenID == "") {
		return Call{}, errors.New("open takes no token id; every other action needs one")
	}
	var id *big.Int
	if p.Action != ActionOpen {
		if id, err = p.tokenID(); err != nil {
			return Call{}, err
		}
	}
	if p.NativeSide < -1 || p.NativeSide > 1 {
		return Call{}, errors.New("invalid native side")
	}
	adds := p.Action == ActionOpen || p.Action == ActionIncrease
	if !adds && (p.NativeSide != -1 && p.Version == V3 || value.Sign() != 0) {
		return Call{}, errors.New("only deposits carry native value")
	}
	var data []byte
	switch p.Version {
	case V3:
		if p.Hooks != "" {
			return Call{}, errors.New("v3 has no hooks")
		}
		if adds {
			var nativeAmount *big.Int
			switch p.NativeSide {
			case 0:
				nativeAmount = desired.Amount0
			case 1:
				nativeAmount = desired.Amount1
			default:
				nativeAmount = new(big.Int)
			}
			if value.Cmp(nativeAmount) != 0 {
				return Call{}, errors.New("native value differs from the native side's desired amount")
			}
		}
		switch p.Action {
		case ActionOpen:
			data = V3Mint(V3MintParams{
				Token0: common.HexToAddress(p.Token0.Address), Token1: common.HexToAddress(p.Token1.Address),
				Fee: big.NewInt(int64(p.Fee)), TickLower: big.NewInt(int64(r.Lower)), TickUpper: big.NewInt(int64(r.Upper)),
				Amount0Desired: desired.Amount0, Amount1Desired: desired.Amount1, Amount0Min: limit.Amount0, Amount1Min: limit.Amount1,
				Recipient: owner, Deadline: deadline.big(),
			})
		case ActionIncrease:
			data = V3Increase(id, desired, limit, deadline)
		case ActionDecrease, ActionClose:
			data = V3Decrease(id, liquidity, limit, deadline, owner, p.Action == ActionClose)
		case ActionCollect:
			data = V3Collect(id, owner)
		default:
			return Call{}, fmt.Errorf("unknown action %q", p.Action)
		}
		if adds && p.NativeSide != -1 {
			data = pack(v3Manager, "multicall", [][]byte{data, refundETH})
		}
	case V4:
		key := p.PoolKey()
		if adds {
			want := new(big.Int)
			if key.Currency0 == (common.Address{}) {
				want = limit.Amount0
			} else if key.Currency1 == (common.Address{}) {
				want = limit.Amount1
			}
			if value.Cmp(want) != 0 {
				return Call{}, errors.New("native value differs from the native side's maximum")
			}
		}
		switch p.Action {
		case ActionOpen:
			data = V4Mint(key, r, liquidity, limit, owner, deadline)
		case ActionIncrease:
			data = V4Increase(key, id, liquidity, limit, owner, deadline)
		case ActionDecrease:
			data = V4Decrease(key, id, liquidity, limit, owner, deadline)
		case ActionCollect:
			data = V4Decrease(key, id, new(big.Int), limit, owner, deadline)
		case ActionClose:
			data = V4Burn(key, id, limit, owner, deadline)
		default:
			return Call{}, fmt.Errorf("unknown action %q", p.Action)
		}
	default:
		return Call{}, fmt.Errorf("unknown version %q", p.Version)
	}
	if p.Action == ActionCollect && (liquidity.Sign() != 0 || limit.Amount0.Sign() != 0 || limit.Amount1.Sign() != 0) {
		return Call{}, errors.New("collect removes no liquidity and has zero minimums")
	}
	return Call{To: common.HexToAddress(p.Manager).Hex(), Data: hexData(data), Value: value}, nil
}

// Allowances is the agent's current approval state for one ERC-20 deposit
// leg. Permit2 fields are v4 only.
type Allowances struct {
	Token             string
	Required          *big.Int
	ERC20             *big.Int // token.allowance(owner, spender) where spender is the manager (v3) or Permit2 (v4)
	Permit2Amount     *big.Int
	Permit2Expiration uint64
}

// permit2Window and permit2Refresh match the web app's approveV4IfNeeded:
// re-approve when less than 30 minutes remain, for 30 days.
const (
	permit2Refresh = 30 * 60
	permit2Window  = 30 * 24 * 60 * 60
)

// Approvals returns the approval calls a deposit needs before the action,
// mirroring the web app: v3 approves the exact amount to the position manager;
// v4 approves Permit2 for the maximum (resetting a nonzero allowance to zero
// first) and then Permit2 for the position manager.
func Approvals(d Deployment, now int64, legs []Allowances) []Call {
	var out []Call
	for _, l := range legs {
		if l.Required.Sign() <= 0 {
			continue
		}
		token := common.HexToAddress(l.Token)
		switch d.Version {
		case V3:
			if l.ERC20.Cmp(l.Required) < 0 {
				out = append(out, Call{To: token.Hex(), Data: hexData(ERC20Approve(d.Manager, l.Required)), Value: new(big.Int)})
			}
		case V4:
			if l.ERC20.Cmp(l.Required) < 0 {
				if l.ERC20.Sign() > 0 {
					out = append(out, Call{To: token.Hex(), Data: hexData(ERC20Approve(d.Permit2, new(big.Int))), Value: new(big.Int)})
				}
				out = append(out, Call{To: token.Hex(), Data: hexData(ERC20Approve(d.Permit2, maxUint256)), Value: new(big.Int)})
			}
			if l.Permit2Amount.Cmp(l.Required) < 0 || l.Permit2Expiration < uint64(now+permit2Refresh) {
				out = append(out, Call{To: d.Permit2.Hex(), Data: hexData(Permit2Approve(token, d.Manager, maxUint160, uint64(now+permit2Window))), Value: new(big.Int)})
			}
		}
	}
	return out
}

// Spend returns the most of each token the action can pull from the agent:
// desired amounts for v3, maximums for v4. Removals spend nothing.
func (p Plan) Spend() (Amounts, error) {
	if p.Action != ActionOpen && p.Action != ActionIncrease {
		return Amounts{new(big.Int), new(big.Int)}, nil
	}
	if p.Version == V4 {
		return p.amounts(p.Limit0, p.Limit1)
	}
	return p.amounts(p.Amount0, p.Amount1)
}

// SameAddress compares hex addresses case-insensitively.
func SameAddress(a, b string) bool { return strings.EqualFold(a, b) }
