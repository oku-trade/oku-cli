package uniswaplp

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// DepositInput is a single-sided deposit request: the amount of one pool
// token, with the other side derived at the current price.
type DepositInput struct {
	Side   Side
	Amount *big.Int
	// UseNative pays the wrapped-native side of a v3 pool in native.
	UseNative bool
}

// planBase fills the fields every plan shares.
func planBase(action Action, d Deployment, owner common.Address, pool Pool, r Range, slippageBps int, deadline int64) (Plan, error) {
	if slippageBps < 0 || slippageBps > MaxSlippageBps {
		return Plan{}, fmt.Errorf("slippage must be 0-%d bps", MaxSlippageBps)
	}
	if pool.Version != d.Version || pool.ChainID != d.ChainID {
		return Plan{}, errors.New("pool is not on this deployment")
	}
	return Plan{
		Action: action, Version: d.Version, ChainID: d.ChainID, Manager: d.Manager.Hex(), Owner: owner.Hex(),
		Token0: pool.Token0, Token1: pool.Token1, Fee: pool.Fee, TickSpacing: pool.TickSpacing, Hooks: pool.Hooks,
		TickLower: r.Lower, TickUpper: r.Upper, SqrtPriceX96: pool.SqrtPriceX96.String(), PoolTick: pool.Tick,
		NativeSide: -1, Value: "0", SlippageBps: slippageBps, Deadline: deadline, LimitKind: "min",
	}, nil
}

func (p *Plan) setDeposit(d Deployment, pool Pool, r Range, in DepositInput) error {
	if in.Amount == nil || in.Amount.Sign() <= 0 {
		return errors.New("deposit amount must be positive")
	}
	if in.Side != Side0 && in.Side != Side1 {
		return errors.New("invalid deposit side")
	}
	state := pool.State()
	precise := true // the web app builds deposits with useFullPrecision
	liquidity, err := LiquidityForAmount(state, r, in.Side, in.Amount, precise)
	if err != nil {
		return err
	}
	desired := MintAmounts(state, r, liquidity)
	// Keep the user's exact input on its side: the derived rounding must not
	// ask for more than they offered.
	if in.Side == Side0 && desired.Amount0.Cmp(in.Amount) > 0 {
		desired.Amount0 = new(big.Int).Set(in.Amount)
	}
	if in.Side == Side1 && desired.Amount1.Cmp(in.Amount) > 0 {
		desired.Amount1 = new(big.Int).Set(in.Amount)
	}
	p.Liquidity = liquidity.String()
	p.Amount0, p.Amount1 = desired.Amount0.String(), desired.Amount1.String()
	switch d.Version {
	case V3:
		mins, err := V3MintMinimums(state, r, desired, p.SlippageBps)
		if err != nil {
			return err
		}
		p.Limit0, p.Limit1, p.LimitKind = mins.Amount0.String(), mins.Amount1.String(), "min"
		if in.UseNative {
			switch d.WrappedNative {
			case common.HexToAddress(p.Token0.Address):
				p.NativeSide, p.Value = 0, desired.Amount0.String()
			case common.HexToAddress(p.Token1.Address):
				p.NativeSide, p.Value = 1, desired.Amount1.String()
			default:
				return errors.New("native deposit needs a pool with the wrapped native token")
			}
		}
	case V4:
		if in.UseNative {
			return errors.New("v4 pays native only in pools whose currency is native")
		}
		maxes, err := V4MintMaximums(state, r, liquidity, p.SlippageBps)
		if err != nil {
			return err
		}
		// The slippage maximum is what v4 can pull, so size liquidity until
		// the input side's maximum fits the amount offered (for native this is
		// the value sent, which must not exceed the spendable balance).
		for range 8 {
			maxIn := maxes.Amount0
			if in.Side == Side1 {
				maxIn = maxes.Amount1
			}
			if maxIn.Cmp(in.Amount) <= 0 {
				break
			}
			liquidity = new(big.Int).Div(new(big.Int).Mul(liquidity, in.Amount), maxIn)
			if liquidity.Sign() <= 0 {
				return errors.New("deposit amount too small for this range")
			}
			if maxes, err = V4MintMaximums(state, r, liquidity, p.SlippageBps); err != nil {
				return err
			}
		}
		if in.Side == Side0 && maxes.Amount0.Cmp(in.Amount) > 0 || in.Side == Side1 && maxes.Amount1.Cmp(in.Amount) > 0 {
			return errors.New("could not fit the slippage maximum within the deposit amount")
		}
		desired = MintAmounts(state, r, liquidity)
		p.Liquidity = liquidity.String()
		p.Amount0, p.Amount1 = desired.Amount0.String(), desired.Amount1.String()
		p.Limit0, p.Limit1, p.LimitKind = maxes.Amount0.String(), maxes.Amount1.String(), "max"
		key := pool.Key()
		if key.Currency0 == (common.Address{}) {
			p.NativeSide, p.Value = 0, maxes.Amount0.String()
		} else if key.Currency1 == (common.Address{}) {
			p.NativeSide, p.Value = 1, maxes.Amount1.String()
		}
	}
	return nil
}

// PlanOpen plans minting a new position.
func PlanOpen(d Deployment, owner common.Address, pool Pool, r Range, in DepositInput, slippageBps int, deadline int64) (Plan, error) {
	if r.Lower%pool.TickSpacing != 0 || r.Upper%pool.TickSpacing != 0 {
		return Plan{}, fmt.Errorf("range ticks must be multiples of the pool tick spacing %d", pool.TickSpacing)
	}
	p, err := planBase(ActionOpen, d, owner, pool, r, slippageBps, deadline)
	if err != nil {
		return Plan{}, err
	}
	if err = p.setDeposit(d, pool, r, in); err != nil {
		return Plan{}, err
	}
	return p, nil
}

func ownedBase(action Action, d Deployment, owner common.Address, pos Position, slippageBps int, deadline int64) (Plan, error) {
	if !SameAddress(pos.Owner, owner.Hex()) {
		return Plan{}, fmt.Errorf("position %s is owned by %s, not the agent", pos.TokenID, pos.Owner)
	}
	p, err := planBase(action, d, owner, pos.Pool, pos.Range, slippageBps, deadline)
	if err != nil {
		return Plan{}, err
	}
	p.TokenID = pos.TokenID
	return p, nil
}

// PlanIncrease plans adding liquidity to an existing position.
func PlanIncrease(d Deployment, owner common.Address, pos Position, in DepositInput, slippageBps int, deadline int64) (Plan, error) {
	p, err := ownedBase(ActionIncrease, d, owner, pos, slippageBps, deadline)
	if err != nil {
		return Plan{}, err
	}
	if err = p.setDeposit(d, pos.Pool, pos.Range, in); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// PlanDecrease plans removing bps/10000 of a position's liquidity and
// collecting what it is owed. Minimums are the web app's getBurnMinimums.
// close removes everything and burns the NFT.
func PlanDecrease(d Deployment, owner common.Address, pos Position, bps int, close bool, slippageBps int, deadline int64) (Plan, error) {
	action := ActionDecrease
	if close {
		action, bps = ActionClose, 10000
	}
	if bps <= 0 || bps > 10000 {
		return Plan{}, errors.New("percent must be above 0 and at most 100")
	}
	p, err := ownedBase(action, d, owner, pos, slippageBps, deadline)
	if err != nil {
		return Plan{}, err
	}
	liquidity := new(big.Int).Mul(pos.Liquidity, big.NewInt(int64(bps)))
	liquidity.Div(liquidity, big.NewInt(10000))
	if !close && liquidity.Sign() == 0 {
		return Plan{}, errors.New("position has no liquidity to remove; use collect or close")
	}
	expected := PositionAmounts(pos.Pool.State(), pos.Range, liquidity)
	mins := BurnMinimums(pos.Pool.SqrtPriceX96, pos.Range, liquidity, slippageBps)
	p.Liquidity = liquidity.String()
	// The removal also collects every uncollected fee, so expected receipts
	// include them. Minimums stay principal-only, like the web app.
	expected.Amount0.Add(expected.Amount0, pos.Fees.Amount0)
	expected.Amount1.Add(expected.Amount1, pos.Fees.Amount1)
	p.Amount0, p.Amount1 = expected.Amount0.String(), expected.Amount1.String()
	p.Limit0, p.Limit1 = mins.Amount0.String(), mins.Amount1.String()
	return p, nil
}

// PlanCollect plans collecting uncollected fees only.
func PlanCollect(d Deployment, owner common.Address, pos Position, deadline int64) (Plan, error) {
	p, err := ownedBase(ActionCollect, d, owner, pos, 0, deadline)
	if err != nil {
		return Plan{}, err
	}
	p.Liquidity, p.Limit0, p.Limit1 = "0", "0", "0"
	p.Amount0, p.Amount1 = pos.Fees.Amount0.String(), pos.Fees.Amount1.String()
	return p, nil
}
