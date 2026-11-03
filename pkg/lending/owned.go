package lending

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Scope is everything a lending transaction may touch for one owner.
type Scope struct {
	Owner common.Address
	// Account is the exact position account: the owner, or for Euler the
	// plan's sub-account. No other sub-account of the owner is accepted.
	Account common.Address
	// Allowed holds the contracts the owner may call or approve: Morpho,
	// Bundler3, GeneralAdapter1, the EVC, and the vaults and tokens in play.
	Allowed map[common.Address]bool
	// Router is the swap target a loop bundle may call with opaque calldata
	// (the chain's Oku router). Zero disallows any swap.
	Router common.Address
	// Hops are bundle intermediaries that may receive tokens mid-bundle
	// (GeneralAdapter1, Bundler3, the Euler Swapper). Swapper and
	// SwapVerifier enable the Euler swap periphery calls.
	Hops                  map[common.Address]bool
	Swapper, SwapVerifier common.Address
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
	// Sold sums router fills by sell and buy token; Bounds records the
	// on-chain swap bounds (skim minimum, debt maximum, supply of the swap
	// remainder) keyed like Summary.
	Sold   map[[2]common.Address]*big.Int
	Bounds map[string]string
}

func newEffects() *Effects {
	return &Effects{Pulled: map[common.Address]*big.Int{}, Approved: map[[2]common.Address]*big.Int{}, Borrowed: map[common.Hash]*big.Int{}, Supplied: map[common.Hash]*big.Int{}, Withdrawn: map[common.Hash]*big.Int{}, Repaid: map[common.Hash]bool{}, VaultCalls: map[common.Address][]string{}, VaultAmounts: map[common.Address]map[string]*big.Int{}, Sold: map[[2]common.Address]*big.Int{}, Bounds: map[string]string{}}
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
	// The owner or the one EVC sub-account the plan selected.
	return a == s.Owner || a == s.Account
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
	// A router approval inside a bundle must cover exactly what the swap
	// sells; it is then accounted for by the sell effect.
	for k, v := range fx.Approved {
		if s.Router == (common.Address{}) || k[1] != s.Router {
			continue
		}
		sold := new(big.Int)
		for pair, amt := range fx.Sold {
			if pair[0] == k[0] {
				sold.Add(sold, amt)
			}
		}
		if v.Cmp(sold) != 0 {
			return nil, fmt.Errorf("router approval %s differs from the %s sold", v, sold)
		}
		delete(fx.Approved, k)
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
	internal := func(a common.Address) bool { return s.owned(a) || depth > 0 && s.Hops[a] }
	switch d.Method {
	case "multicall":
		var entries []bundleEntry
		abi.ConvertType(d.Args[0], &entries)
		for _, e := range entries {
			if e.Value.Sign() != 0 {
				return errors.New("bundle entry carries native value")
			}
			if s.Router != (common.Address{}) && e.To == s.Router && depth > 0 {
				if err := s.routerFill(e.Data, fx); err != nil {
					return err
				}
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
		if amt(1).Cmp(maxUint256) == 0 {
			// Supply whatever the swap delivered beyond its minimum.
			fx.Bounds["supply_rest:"+marketID(d.Args[0]).Hex()] = "true"
			break
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
		if !internal(addr(2)) {
			return errors.New("collateral receiver is not the owner")
		}
		add(fx.Withdrawn, marketID(d.Args[0]), amt(1))
	case "withdrawCollateral":
		if !s.owned(addr(2)) || !s.owned(addr(3)) {
			return errors.New("collateral withdrawal is not to the owner")
		}
		add(fx.Withdrawn, marketID(d.Args[0]), amt(1))
	case "withdraw", "redeem":
		if !internal(addr(1)) || !s.owned(addr(2)) {
			return fmt.Errorf("%s receiver or owner is not the owner", d.Method)
		}
		fx.vault(to, d.Method, amt(0))
	case "borrow":
		if !internal(addr(1)) {
			return errors.New("borrow receiver is not the owner")
		}
		fx.vault(to, d.Method, amt(0))
	case "repay", "deposit":
		// EVault calls: (amount, receiver); for repay the receiver is the debtor.
		if !s.owned(addr(1)) {
			return fmt.Errorf("%s account is not the owner", d.Method)
		}
		fx.vault(to, d.Method, amt(0))
	case "approve":
		if !s.Allowed[addr(0)] && !(depth > 0 && addr(0) == s.Router && s.Router != (common.Address{})) {
			return fmt.Errorf("approval spender %s is not part of this action", addr(0).Hex())
		}
		if amt(1).Sign() > 0 {
			// Zero resets (for USDT-style tokens) grant nothing.
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
		if depth > 0 && to == s.Swapper && s.Swapper != (common.Address{}) || to == s.SwapVerifier && s.SwapVerifier != (common.Address{}) {
			return s.eulerSwap(to, d, depth, fx)
		}
		return fmt.Errorf("%s is not allowed in a lending transaction", d.Method)
	}
	return nil
}

// routerFill checks an Oku Router fill inside a bundle: it must pay a
// bundle hop (GeneralAdapter1 or the Euler Swapper), never anyone else.
func (s Scope) routerFill(data []byte, fx *Effects) error {
	t, err := DecodeRouterTrade(data)
	if err != nil {
		return err
	}
	if !s.Hops[t.Recipient] || !s.Allowed[t.Sell] || !s.Allowed[t.Buy] {
		return fmt.Errorf("router fill pays %s or trades a token outside this action", t.Recipient.Hex())
	}
	fx.Swaps++
	k := [2]common.Address{t.Sell, t.Buy}
	if fx.Sold[k] == nil {
		fx.Sold[k] = new(big.Int)
	}
	fx.Sold[k].Add(fx.Sold[k], t.Amount)
	return nil
}

// eulerSwap checks Euler Swapper and SwapVerifier calls (evk-periphery):
// the generic handler may only call the Oku Router, and every account,
// receiver and vault is the owner's or part of this action.
func (s Scope) eulerSwap(to common.Address, d Decoded, depth int, fx *Effects) error {
	addr := func(i int) common.Address { return d.Args[i].(common.Address) }
	amt := func(i int) *big.Int { return d.Args[i].(*big.Int) }
	vault := func(a common.Address) bool { return s.Allowed[a] && !s.Hops[a] }
	swapper := to == s.Swapper
	switch {
	case swapper && d.Method == "loop:multicall":
		for _, c := range d.Args[0].([][]byte) {
			if err := s.check(to, c, depth+1, fx); err != nil {
				return err
			}
		}
	case swapper && d.Method == "loop:swap":
		p := *abi.ConvertType(d.Args[0], new(swapParams)).(*swapParams)
		if p.Handler != genericHandler || p.Mode.Sign() != 0 || !s.owned(p.Account) || !s.owned(p.AccountIn) || !vault(p.VaultIn) || !vault(p.Receiver) || !s.Allowed[p.TokenIn] || !s.Allowed[p.TokenOut] {
			return errors.New("euler swap is not an exact-in swap for the owner")
		}
		args, err := genericArgs.Unpack(p.Data)
		if err != nil {
			return err
		}
		if args[0].(common.Address) != s.Router || s.Router == (common.Address{}) {
			return errors.New("euler swap target is not the Oku Router")
		}
		data := args[1].([]byte)
		t, err := DecodeRouterTrade(data)
		if err != nil || t.Sell != p.TokenIn || t.Buy != p.TokenOut {
			return errors.New("euler swap router fill differs from the swap")
		}
		return s.routerFill(data, fx)
	case swapper && d.Method == "loop:sweep":
		if !s.owned(addr(2)) && !vault(addr(2)) {
			return errors.New("swapper sweep pays outside this action")
		}
	case swapper && d.Method == "loop:deposit":
		if !vault(addr(1)) || !s.owned(addr(3)) {
			return errors.New("swapper deposit is not for the owner")
		}
	case swapper && d.Method == "loop:repay":
		if !vault(addr(1)) || !s.owned(addr(3)) {
			return errors.New("swapper repay is not for the owner")
		}
		fx.vault(addr(1), "swap_repay", amt(2))
	case !swapper && d.Method == "loop:verifyAmountMinAndSkim":
		if !vault(addr(0)) || !s.owned(addr(1)) {
			return errors.New("skim is not for the owner")
		}
		fx.Bounds["skim_min:"+strings.ToLower(addr(0).Hex())] = amt(2).String()
	case !swapper && d.Method == "loop:verifyDebtMax":
		if !vault(addr(0)) || !s.owned(addr(1)) {
			return errors.New("debt check is not for the owner")
		}
		fx.Bounds["debt_max:"+strings.ToLower(addr(0).Hex())] = amt(2).String()
	default:
		return fmt.Errorf("%s is not allowed in a lending transaction", d.Method)
	}
	return nil
}
