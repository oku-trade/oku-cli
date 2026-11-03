package lending

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Plan is a prepared lending action. It is stored in the intent and bound
// into its digest. Submit rebuilds the Scope from it and requires the
// sealed calldata to have exactly the Expect effects.
type Plan struct {
	Protocol        string           `json:"protocol"` // morpho or euler
	Action          string           `json:"action"`
	Chain           uint64           `json:"chain_id"`
	Owner           common.Address   `json:"owner"`
	MarketID        *common.Hash     `json:"market_id,omitempty"`
	Vault           *common.Address  `json:"vault,omitempty"`            // earn vault or Euler borrow vault
	CollateralVault *common.Address  `json:"collateral_vault,omitempty"` // Euler
	SubAccount      uint8            `json:"sub_account,omitempty"`
	Tokens          []common.Address `json:"tokens"`
	Router          *common.Address  `json:"router,omitempty"` // loop swap target
	Expect          Want             `json:"expect"`
	// Risk is the projected post-action position shown at prepare. It is
	// informational and re-derived on chain at submit for borrow actions.
	Risk *Risk `json:"risk,omitempty"`
}

// Scope is every contract this plan may touch.
func (p Plan) Scope() (Scope, error) {
	s := Scope{Owner: p.Owner, Allowed: map[common.Address]bool{}}
	switch p.Protocol {
	case "morpho":
		d, err := Morpho(p.Chain)
		if err != nil {
			return s, err
		}
		s.Allowed[d.Morpho], s.Allowed[d.Bundler3], s.Allowed[d.GeneralAdapter1] = true, true, true
	case "euler":
		evc, err := Euler(p.Chain)
		if err != nil {
			return s, err
		}
		s.Allowed[evc] = true
	default:
		return s, fmt.Errorf("unknown protocol %q", p.Protocol)
	}
	for _, a := range []*common.Address{p.Vault, p.CollateralVault} {
		if a != nil {
			s.Allowed[*a] = true
		}
	}
	for _, t := range p.Tokens {
		s.Allowed[t] = true
	}
	if p.Router != nil {
		s.Router = *p.Router
	}
	return s, nil
}

// Summary flattens effects into a canonical map: amounts are decimal raw
// units, keys name the token, market or vault. Approvals are excluded:
// their spender is constrained by the Scope and their amount by the pull.
func (fx *Effects) Summary() map[string]string {
	out := map[string]string{}
	put := func(k string, v *big.Int) {
		if v != nil && v.Sign() > 0 {
			out[k] = v.String()
		}
	}
	for t, v := range fx.Pulled {
		put("pull:"+strings.ToLower(t.Hex()), v)
	}
	for id, v := range fx.Borrowed {
		put("borrow:"+id.Hex(), v)
	}
	for id, v := range fx.Supplied {
		put("supply:"+id.Hex(), v)
	}
	for id, v := range fx.Withdrawn {
		put("withdraw:"+id.Hex(), v)
	}
	for id := range fx.Repaid {
		out["repay:"+id.Hex()] = "true"
	}
	for v, methods := range fx.VaultAmounts {
		for m, amt := range methods {
			put("vault:"+strings.ToLower(v.Hex())+":"+m, amt)
		}
	}
	for v, methods := range fx.VaultCalls {
		for _, m := range methods {
			if m == "disableController" {
				out["vault:"+strings.ToLower(v.Hex())+":"+m] = "true"
			}
		}
	}
	if fx.Swaps > 0 {
		out["swaps"] = fmt.Sprint(fx.Swaps)
	}
	if fx.FlashLoans > 0 {
		out["flash_loans"] = fmt.Sprint(fx.FlashLoans)
	}
	if fx.Authorizes {
		out["authorize"] = "true"
	}
	return out
}

// Verify checks calls against the plan's scope and expected effects.
func (p Plan) Verify(calls []Call) error {
	if len(p.Expect) == 0 {
		return errors.New("plan has no expected effects")
	}
	s, err := p.Scope()
	if err != nil {
		return err
	}
	fx, err := CheckCalls(calls, s)
	if err != nil {
		return err
	}
	return sameSummary(fx.Summary(), p.Expect)
}

// Want builds the effects a request must produce, in Summary's keys.
type Want map[string]string

func (w Want) set(k string, v *big.Int) Want {
	if v != nil && v.Sign() > 0 {
		w[k] = v.String()
	}
	return w
}

// Pull expects amount of token transferred from the owner into a bundle.
func (w Want) Pull(token common.Address, amount *big.Int) Want {
	return w.set("pull:"+strings.ToLower(token.Hex()), amount)
}

// Vault expects method on an ERC-4626 vault or EVault with amount.
func (w Want) Vault(vault common.Address, method string, amount *big.Int) Want {
	return w.set("vault:"+strings.ToLower(vault.Hex())+":"+method, amount)
}

// Flag expects a boolean effect such as a full repay or controller release.
func (w Want) Flag(k string) Want { w[k] = "true"; return w }

// Supply, Borrow and Withdraw expect Morpho Blue collateral and debt moves.
func (w Want) Supply(id common.Hash, v *big.Int) Want   { return w.set("supply:"+id.Hex(), v) }
func (w Want) Borrow(id common.Hash, v *big.Int) Want   { return w.set("borrow:"+id.Hex(), v) }
func (w Want) Withdraw(id common.Hash, v *big.Int) Want { return w.set("withdraw:"+id.Hex(), v) }

// Repaid expects a Morpho Blue repay on the market.
func (w Want) Repaid(id common.Hash) Want { return w.Flag("repay:" + id.Hex()) }

// Check derives the effects of calls under the plan's scope and requires
// them to equal want. Prepare uses it before sealing.
func (p Plan) Check(calls []Call, want Want) error {
	p.Expect = want
	return p.Verify(calls)
}

func sameSummary(got, want map[string]string) error {
	var diff []string
	for k, v := range want {
		if got[k] != v {
			diff = append(diff, fmt.Sprintf("%s: want %s, got %s", k, v, orNone(got[k])))
		}
	}
	for k, v := range got {
		if _, ok := want[k]; !ok {
			diff = append(diff, fmt.Sprintf("%s: unexpected %s", k, v))
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		return errors.New("calldata effects differ from the request: " + strings.Join(diff, "; "))
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
