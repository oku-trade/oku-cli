package lending

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Scope is everything a lending transaction may touch for one owner.
type Scope struct {
	Owner common.Address
	// Allowed holds the contracts the owner may call or approve: Morpho,
	// Bundler3, GeneralAdapter1, the EVC, and the vaults and tokens in play.
	Allowed map[common.Address]bool
	// Router is the swap target a loop bundle may call with opaque calldata
	// (the chain's Oku router). Zero disallows any swap.
	Router common.Address
}

// Effects sums what verified calldata does, per token or market, in raw
// units. Prepare and submit compare it with the requested action.
type Effects struct {
	Pulled     map[common.Address]*big.Int // tokens transferred from the owner
	Approved   map[[2]common.Address]*big.Int
	Borrowed   map[common.Hash]*big.Int // Morpho market id -> assets borrowed
	Supplied   map[common.Hash]*big.Int // Morpho market id -> collateral supplied
	Withdrawn  map[common.Hash]*big.Int // Morpho market id -> collateral withdrawn
	Repaid     map[common.Hash]bool
	VaultCalls map[common.Address][]string // ERC-4626 / EVault -> methods
	// VaultAmounts sums EVault and ERC-4626 call amounts by vault and method
	// (deposit, withdraw, borrow, repay assets; redeem shares).
	VaultAmounts map[common.Address]map[string]*big.Int
	Swaps        int
	FlashLoans   int
	Authorizes   bool
}

func newEffects() *Effects {
	return &Effects{Pulled: map[common.Address]*big.Int{}, Approved: map[[2]common.Address]*big.Int{}, Borrowed: map[common.Hash]*big.Int{}, Supplied: map[common.Hash]*big.Int{}, Withdrawn: map[common.Hash]*big.Int{}, Repaid: map[common.Hash]bool{}, VaultCalls: map[common.Address][]string{}, VaultAmounts: map[common.Address]map[string]*big.Int{}}
}

func (fx *Effects) vault(v common.Address, method string, amount *big.Int) {
	fx.VaultCalls[v] = append(fx.VaultCalls[v], method)
	if amount == nil {
		return
	}
	if fx.VaultAmounts[v] == nil {
		fx.VaultAmounts[v] = map[string]*big.Int{}
	}
	if fx.VaultAmounts[v][method] == nil {
		fx.VaultAmounts[v][method] = new(big.Int)
	}
	fx.VaultAmounts[v][method].Add(fx.VaultAmounts[v][method], amount)
}

// VaultAmount is the summed amount of method calls on vault, or nil.
func (fx *Effects) VaultAmount(v common.Address, method string) *big.Int {
	return fx.VaultAmounts[v][method]
}

func add(m map[common.Hash]*big.Int, k common.Hash, v *big.Int) {
	if m[k] == nil {
		m[k] = new(big.Int)
	}
	m[k].Add(m[k], v)
}

func (s Scope) owned(a common.Address) bool {
	// The owner or one of its EVC sub-accounts (same first 19 bytes).
	return a == s.Owner || bytes.Equal(a[:19], s.Owner[:19])
}

// CheckCalls re-verifies lending calldata before it is sealed or sent:
// every call and nested bundle, flash-loan or EVC batch item decodes
// against the lending ABI and targets an allowed contract, and every
// receiver, on-behalf account and debtor is the owner (or its
// sub-accounts), or GeneralAdapter1/Bundler3 inside a bundle.
func CheckCalls(calls []Call, s Scope) (*Effects, error) {
	fx := newEffects()
	for _, c := range calls {
		if c.Value != nil && c.Value.Sign() != 0 {
			return nil, errors.New("lending call carries native value")
		}
		if !s.Allowed[c.To] {
			return nil, fmt.Errorf("call target %s is not part of this action", c.To.Hex())
		}
		if err := s.check(c.To, c.Data, 0, fx); err != nil {
			return nil, err
		}
	}
	return fx, nil
}

type bundleEntry struct {
	To           common.Address
	Data         []byte
	Value        *big.Int
	SkipRevert   bool
	CallbackHash [32]byte
}

func marketID(v any) common.Hash {
	mp := *abi.ConvertType(v, new(MarketParams)).(*MarketParams)
	return mp.ID()
}

func (s Scope) check(to common.Address, data []byte, depth int, fx *Effects) error {
	if depth > 3 {
		return errors.New("nesting too deep")
	}
	d, err := Decode(to, data)
	if err != nil {
		return err
	}
	addr := func(i int) common.Address { return d.Args[i].(common.Address) }
	amt := func(i int) *big.Int { return d.Args[i].(*big.Int) }
	internal := func(a common.Address) bool { return s.owned(a) || depth > 0 && s.Allowed[a] }
	switch d.Method {
	case "multicall":
		var entries []bundleEntry
		abi.ConvertType(d.Args[0], &entries)
		for _, e := range entries {
			if e.Value.Sign() != 0 {
				return errors.New("bundle entry carries native value")
			}
			if s.Router != (common.Address{}) && e.To == s.Router && depth > 0 {
				fx.Swaps++ // opaque swap, bounded by the collateral supply that follows
				continue
			}
			if !s.Allowed[e.To] {
				return fmt.Errorf("bundle target %s is not part of this action", e.To.Hex())
			}
			if err := s.check(e.To, e.Data, depth+1, fx); err != nil {
				return err
			}
		}
	case "morphoFlashLoan":
		// The callback is an abi-encoded bundle, checked like a multicall.
		fx.FlashLoans++
		wrapped := append(append([]byte{}, morphoABI.Methods["multicall"].ID...), d.Args[2].([]byte)...)
		return s.check(to, wrapped, depth+1, fx)
	case "batch":
		items, err := DecodeEVCBatch(data, to)
		if err != nil {
			return err
		}
		for _, it := range items {
			if !s.Allowed[it.Call.To] && it.Call.To != s.Router || it.OnBehalf != (common.Address{}) && !s.owned(it.OnBehalf) {
				return fmt.Errorf("EVC item %s on %s is not owned", it.Call.Method, it.Call.To.Hex())
			}
			if err := s.check(it.Call.To, it.Data, depth+1, fx); err != nil {
				return err
			}
		}
	case "erc20TransferFrom":
		if !internal(addr(1)) {
			return fmt.Errorf("transferFrom pays %s", addr(1).Hex())
		}
		if fx.Pulled[addr(0)] == nil {
			fx.Pulled[addr(0)] = new(big.Int)
		}
		fx.Pulled[addr(0)].Add(fx.Pulled[addr(0)], amt(2))
	case "erc20Transfer":
		if !internal(addr(1)) {
			return fmt.Errorf("transfer pays %s", addr(1).Hex())
		}
	case "erc4626Deposit":
		if !s.owned(addr(3)) {
			return errors.New("vault deposit receiver is not the owner")
		}
		fx.vault(addr(0), "deposit", amt(1))
	case "morphoSupplyCollateral":
		if !s.owned(addr(2)) {
			return errors.New("collateral is not supplied for the owner")
		}
		add(fx.Supplied, marketID(d.Args[0]), amt(1))
	case "morphoRepay":
		if !s.owned(addr(4)) {
			return errors.New("repay is not on behalf of the owner")
		}
		fx.Repaid[marketID(d.Args[0])] = true
	case "morphoBorrow":
		if !internal(addr(4)) {
			return errors.New("borrow receiver is not the owner")
		}
		add(fx.Borrowed, marketID(d.Args[0]), amt(1))
	case "morphoWithdrawCollateral":
		if !s.owned(addr(2)) {
			return errors.New("collateral receiver is not the owner")
		}
		add(fx.Withdrawn, marketID(d.Args[0]), amt(1))
	case "withdrawCollateral":
		if !s.owned(addr(2)) || !s.owned(addr(3)) {
			return errors.New("collateral withdrawal is not to the owner")
		}
		add(fx.Withdrawn, marketID(d.Args[0]), amt(1))
	case "withdraw", "redeem":
		if !s.owned(addr(1)) || !s.owned(addr(2)) {
			return fmt.Errorf("%s receiver or owner is not the owner", d.Method)
		}
		fx.vault(to, d.Method, amt(0))
	case "repay", "deposit", "borrow":
		// EVault calls: (amount, receiver); for repay the receiver is the debtor.
		if !s.owned(addr(1)) {
			return fmt.Errorf("%s account is not the owner", d.Method)
		}
		fx.vault(to, d.Method, amt(0))
	case "approve":
		if !s.Allowed[addr(0)] && !(depth > 0 && addr(0) == s.Router && s.Router != (common.Address{})) {
			return fmt.Errorf("approval spender %s is not part of this action", addr(0).Hex())
		}
		if depth == 0 {
			fx.Approved[[2]common.Address{to, addr(0)}] = amt(1)
		}
	case "setAuthorization":
		if !s.Allowed[addr(0)] || !d.Args[1].(bool) {
			return errors.New("authorization is not for an allowed adapter")
		}
		fx.Authorizes = true
	case "enableCollateral", "enableController":
		if !s.owned(addr(0)) || !s.Allowed[addr(1)] {
			return fmt.Errorf("%s is not for an owned account and allowed vault", d.Method)
		}
	case "disableController":
		fx.vault(to, d.Method, nil)
	default:
		return fmt.Errorf("%s is not allowed in a lending transaction", d.Method)
	}
	return nil
}
