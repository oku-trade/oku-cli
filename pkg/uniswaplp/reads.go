package uniswaplp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/onchain"
)

// Caller batches chain reads. onchain.HTTPRPC satisfies it with one
// Multicall3 round trip per call.
type Caller interface {
	Aggregate(ctx context.Context, chain uint64, calls []onchain.Call3) ([]onchain.Result3, error)
}

type read struct {
	target common.Address
	abi    abi.ABI
	method string
	args   []any
}

func (r read) call() onchain.Call3 {
	return onchain.Call3{Target: r.target.Hex(), Data: pack(r.abi, r.method, r.args...)}
}

// batch runs reads in one round and unpacks each result. A failed or
// malformed read is an error naming the method: callers never see a zero
// standing in for missing data.
func batch(ctx context.Context, c Caller, chain uint64, reads []read) ([][]any, error) {
	calls := make([]onchain.Call3, len(reads))
	for i, r := range reads {
		calls[i] = r.call()
	}
	res, err := c.Aggregate(ctx, chain, calls)
	if err != nil {
		return nil, err
	}
	if len(res) != len(reads) {
		return nil, fmt.Errorf("multicall returned %d results for %d reads", len(res), len(reads))
	}
	out := make([][]any, len(reads))
	for i, r := range reads {
		if !res[i].Success {
			return nil, fmt.Errorf("%s on %s reverted", r.method, r.target.Hex())
		}
		vals, err := r.abi.Unpack(r.method, res[i].Data)
		if err != nil {
			return nil, fmt.Errorf("decoding %s on %s: %w", r.method, r.target.Hex(), err)
		}
		out[i] = vals
	}
	return out, nil
}

func asBig(v any) *big.Int {
	switch x := v.(type) {
	case *big.Int:
		return x
	case uint8:
		return big.NewInt(int64(x))
	case uint16:
		return big.NewInt(int64(x))
	case uint32:
		return big.NewInt(int64(x))
	case uint64:
		return new(big.Int).SetUint64(x)
	}
	return nil
}

func asInt(v any) (int, error) {
	b := asBig(v)
	if b == nil || !b.IsInt64() {
		return 0, fmt.Errorf("unexpected integer %v", v)
	}
	return int(b.Int64()), nil
}

// Pool is the on-chain state of a pool, enough to choose a range.
type Pool struct {
	Version      Version  `json:"version"`
	ChainID      uint64   `json:"chain_id"`
	Address      string   `json:"pool,omitempty"`    // v3 pool contract
	PoolID       string   `json:"pool_id,omitempty"` // v4 pool id
	Token0       Token    `json:"token0"`
	Token1       Token    `json:"token1"`
	Fee          int      `json:"fee"`
	TickSpacing  int      `json:"tick_spacing"`
	Hooks        string   `json:"hooks,omitempty"`
	SqrtPriceX96 *big.Int `json:"-"`
	Tick         int      `json:"tick"`
	Liquidity    *big.Int `json:"-"`
}

// State returns the pool price as a PoolState.
func (p Pool) State() PoolState { return PoolState{p.SqrtPriceX96, p.Tick} }

// Key is the v4 pool key.
func (p Pool) Key() PoolKey {
	return PoolKey{common.HexToAddress(p.Token0.Address), common.HexToAddress(p.Token1.Address), big.NewInt(int64(p.Fee)), big.NewInt(int64(p.TickSpacing)), common.HexToAddress(p.Hooks)}
}

// ErrPoolNotFound reports a pool that does not exist or is not initialized.
var ErrPoolNotFound = errors.New("pool not found or not initialized")

// SortTokens orders two token addresses as Uniswap does (lower address first).
func SortTokens(a, b common.Address) (common.Address, common.Address, error) {
	switch bytes.Compare(a.Bytes(), b.Bytes()) {
	case 0:
		return common.Address{}, common.Address{}, errors.New("pool tokens must differ")
	case 1:
		return b, a, nil
	}
	return a, b, nil
}

// PoolQuery identifies a pool by its tokens and fee tier. TickSpacing and
// Hooks are v4 only; a zero tick spacing uses the web app's default for the
// fee tier.
type PoolQuery struct {
	Token0, Token1 common.Address
	Fee            int
	TickSpacing    int
	Hooks          common.Address
}

// LoadPool reads a pool's identity and current price.
func LoadPool(ctx context.Context, c Caller, d Deployment, q PoolQuery) (Pool, error) {
	t0, t1, err := SortTokens(q.Token0, q.Token1)
	if err != nil {
		return Pool{}, err
	}
	if q.Fee < 0 || q.Fee >= 1<<24 {
		return Pool{}, fmt.Errorf("invalid fee tier %d", q.Fee)
	}
	switch d.Version {
	case V3:
		if t0 == (common.Address{}) {
			return Pool{}, errors.New("v3 pools hold the wrapped native token, not native")
		}
		res, err := batch(ctx, c, d.ChainID, []read{{d.Factory, v3Pool, "getPool", []any{t0, t1, big.NewInt(int64(q.Fee))}}})
		if err != nil {
			return Pool{}, err
		}
		addr, ok := res[0][0].(common.Address)
		if !ok || addr == (common.Address{}) {
			return Pool{}, ErrPoolNotFound
		}
		return loadV3Pool(ctx, c, d, addr)
	case V4:
		spacing := q.TickSpacing
		if spacing == 0 {
			var ok bool
			if spacing, ok = V4DefaultTickSpacing(q.Fee); !ok {
				return Pool{}, fmt.Errorf("fee tier %d has no default tick spacing; pass the tick spacing", q.Fee)
			}
		}
		key := PoolKey{t0, t1, big.NewInt(int64(q.Fee)), big.NewInt(int64(spacing)), q.Hooks}
		return loadV4Pool(ctx, c, d, key)
	}
	return Pool{}, fmt.Errorf("unknown version %q", d.Version)
}

func loadV3Pool(ctx context.Context, c Caller, d Deployment, addr common.Address) (Pool, error) {
	res, err := batch(ctx, c, d.ChainID, []read{
		{addr, v3Pool, "slot0", nil}, {addr, v3Pool, "liquidity", nil}, {addr, v3Pool, "tickSpacing", nil},
		{addr, v3Pool, "fee", nil}, {addr, v3Pool, "token0", nil}, {addr, v3Pool, "token1", nil},
	})
	if err != nil {
		return Pool{}, err
	}
	sqrtP := asBig(res[0][0])
	tick, err := asInt(res[0][1])
	if err != nil {
		return Pool{}, err
	}
	spacing, err := asInt(res[2][0])
	if err != nil {
		return Pool{}, err
	}
	fee, err := asInt(res[3][0])
	if err != nil {
		return Pool{}, err
	}
	if sqrtP == nil || sqrtP.Sign() == 0 {
		return Pool{}, ErrPoolNotFound
	}
	t0, _ := res[4][0].(common.Address)
	t1, _ := res[5][0].(common.Address)
	return Pool{Version: V3, ChainID: d.ChainID, Address: addr.Hex(), Token0: Token{Address: t0.Hex()}, Token1: Token{Address: t1.Hex()},
		Fee: fee, TickSpacing: spacing, SqrtPriceX96: sqrtP, Tick: tick, Liquidity: asBig(res[1][0])}, nil
}

func loadV4Pool(ctx context.Context, c Caller, d Deployment, key PoolKey) (Pool, error) {
	id := key.PoolID()
	res, err := batch(ctx, c, d.ChainID, []read{{d.StateView, v4, "getSlot0", []any{id}}, {d.StateView, v4, "getLiquidity", []any{id}}})
	if err != nil {
		return Pool{}, err
	}
	sqrtP := asBig(res[0][0])
	if sqrtP == nil || sqrtP.Sign() == 0 {
		return Pool{}, ErrPoolNotFound
	}
	tick, err := asInt(res[0][1])
	if err != nil {
		return Pool{}, err
	}
	hooks := ""
	if key.Hooks != (common.Address{}) {
		hooks = key.Hooks.Hex()
	}
	return Pool{Version: V4, ChainID: d.ChainID, PoolID: id.Hex(), Token0: Token{Address: key.Currency0.Hex()}, Token1: Token{Address: key.Currency1.Hex()},
		Fee: int(key.Fee.Int64()), TickSpacing: int(key.TickSpacing.Int64()), Hooks: hooks, SqrtPriceX96: sqrtP, Tick: tick, Liquidity: asBig(res[1][0])}, nil
}

// Position is one liquidity position NFT with its pool and live amounts.
type Position struct {
	TokenID   string   `json:"token_id"`
	Owner     string   `json:"owner"`
	Pool      Pool     `json:"pool"`
	Range     Range    `json:"-"`
	Liquidity *big.Int `json:"-"`
	// Principal is what removing all liquidity returns at the current price.
	Principal Amounts `json:"-"`
	// Fees are uncollected fees owed to the position.
	Fees Amounts `json:"-"`
}

// InRange reports whether the position currently earns fees.
func (p Position) InRange() bool { return p.Range.InRange(p.Pool.Tick) }

// feeGrowthInside is the v3 core Tick.getFeeGrowthInside.
func feeGrowthInside(tick, lower, upper int, global, outLower, outUpper *big.Int) *big.Int {
	below := outLower
	if tick < lower {
		below = new(big.Int).Sub(global, outLower)
	}
	above := outUpper
	if tick >= upper {
		above = new(big.Int).Sub(global, outUpper)
	}
	v := new(big.Int).Sub(global, below)
	v.Sub(v, above)
	return v.Mod(v, two256)
}

type v3Raw struct {
	id                        *big.Int
	owner                     common.Address
	token0, token1            common.Address
	fee, tickLower, tickUpper int
	liquidity, last0, last1   *big.Int
	owed0, owed1              *big.Int
}

// LoadV3Positions reads v3 positions by token id in a fixed number of batched
// rounds regardless of how many ids are asked for.
func LoadV3Positions(ctx context.Context, c Caller, d Deployment, ids []*big.Int) ([]Position, error) {
	if len(ids) == 0 {
		return []Position{}, nil
	}
	reads := make([]read, 0, len(ids)*2)
	for _, id := range ids {
		reads = append(reads, read{d.Manager, v3Manager, "positions", []any{id}}, read{d.Manager, v3Manager, "ownerOf", []any{id}})
	}
	res, err := batch(ctx, c, d.ChainID, reads)
	if err != nil {
		return nil, err
	}
	raws := make([]v3Raw, len(ids))
	type poolID struct {
		t0, t1 common.Address
		fee    int
	}
	poolIndex := map[poolID]int{}
	var poolReads []read
	var poolKeys []poolID
	for i, id := range ids {
		p, o := res[i*2], res[i*2+1]
		r := v3Raw{id: id, owner: o[0].(common.Address), token0: p[2].(common.Address), token1: p[3].(common.Address), liquidity: asBig(p[7]), last0: asBig(p[8]), last1: asBig(p[9]), owed0: asBig(p[10]), owed1: asBig(p[11])}
		if r.fee, err = asInt(p[4]); err != nil {
			return nil, err
		}
		if r.tickLower, err = asInt(p[5]); err != nil {
			return nil, err
		}
		if r.tickUpper, err = asInt(p[6]); err != nil {
			return nil, err
		}
		raws[i] = r
		k := poolID{r.token0, r.token1, r.fee}
		if _, ok := poolIndex[k]; !ok {
			poolIndex[k] = len(poolKeys)
			poolKeys = append(poolKeys, k)
			poolReads = append(poolReads, read{d.Factory, v3Pool, "getPool", []any{r.token0, r.token1, big.NewInt(int64(r.fee))}})
		}
	}
	addrRes, err := batch(ctx, c, d.ChainID, poolReads)
	if err != nil {
		return nil, err
	}
	addrs := make([]common.Address, len(poolKeys))
	stateReads := make([]read, 0, len(poolKeys)*4+len(raws)*2)
	for i := range poolKeys {
		addrs[i] = addrRes[i][0].(common.Address)
		a := addrs[i]
		stateReads = append(stateReads, read{a, v3Pool, "slot0", nil}, read{a, v3Pool, "tickSpacing", nil}, read{a, v3Pool, "feeGrowthGlobal0X128", nil}, read{a, v3Pool, "feeGrowthGlobal1X128", nil})
	}
	tickBase := len(stateReads)
	for _, r := range raws {
		a := addrs[poolIndex[poolID{r.token0, r.token1, r.fee}]]
		stateReads = append(stateReads, read{a, v3Pool, "ticks", []any{big.NewInt(int64(r.tickLower))}}, read{a, v3Pool, "ticks", []any{big.NewInt(int64(r.tickUpper))}})
	}
	st, err := batch(ctx, c, d.ChainID, stateReads)
	if err != nil {
		return nil, err
	}
	out := make([]Position, len(raws))
	for i, r := range raws {
		pi := poolIndex[poolID{r.token0, r.token1, r.fee}]
		slot, spacingRaw, g0, g1 := st[pi*4], st[pi*4+1], asBig(st[pi*4+2][0]), asBig(st[pi*4+3][0])
		tick, err := asInt(slot[1])
		if err != nil {
			return nil, err
		}
		spacing, err := asInt(spacingRaw[0])
		if err != nil {
			return nil, err
		}
		lo, hi := st[tickBase+i*2], st[tickBase+i*2+1]
		in0 := feeGrowthInside(tick, r.tickLower, r.tickUpper, g0, asBig(lo[2]), asBig(hi[2]))
		in1 := feeGrowthInside(tick, r.tickLower, r.tickUpper, g1, asBig(lo[3]), asBig(hi[3]))
		fees := UncollectedFees(r.liquidity, in0, in1, r.last0, r.last1)
		fees.Amount0.Add(fees.Amount0, r.owed0)
		fees.Amount1.Add(fees.Amount1, r.owed1)
		pool := Pool{Version: V3, ChainID: d.ChainID, Address: addrs[pi].Hex(), Token0: Token{Address: r.token0.Hex()}, Token1: Token{Address: r.token1.Hex()}, Fee: r.fee, TickSpacing: spacing, SqrtPriceX96: asBig(slot[0]), Tick: tick}
		rg := Range{r.tickLower, r.tickUpper}
		out[i] = Position{TokenID: r.id.String(), Owner: r.owner.Hex(), Pool: pool, Range: rg, Liquidity: r.liquidity, Principal: PositionAmounts(pool.State(), rg, r.liquidity), Fees: fees}
	}
	return out, nil
}

// V3OwnedTokenIDs enumerates the v3 position NFTs owner holds (ERC-721
// enumerable), at most limit of them.
func V3OwnedTokenIDs(ctx context.Context, c Caller, d Deployment, owner common.Address, limit int) ([]*big.Int, int, error) {
	res, err := batch(ctx, c, d.ChainID, []read{{d.Manager, v3Manager, "balanceOf", []any{owner}}})
	if err != nil {
		return nil, 0, err
	}
	n := asBig(res[0][0])
	if n == nil || !n.IsInt64() {
		return nil, 0, errors.New("invalid position count")
	}
	total := int(n.Int64())
	count := min(total, limit)
	reads := make([]read, count)
	for i := range reads {
		reads[i] = read{d.Manager, v3Manager, "tokenOfOwnerByIndex", []any{owner, big.NewInt(int64(i))}}
	}
	idRes, err := batch(ctx, c, d.ChainID, reads)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]*big.Int, count)
	for i := range ids {
		ids[i] = asBig(idRes[i][0])
	}
	return ids, total, nil
}

func int24At(info *big.Int, shift uint) int {
	v := new(big.Int).Rsh(info, shift)
	v.And(v, big.NewInt(0xffffff))
	x := int(v.Int64())
	if x >= 0x800000 {
		x -= 0x1000000
	}
	return x
}

// LoadV4Positions reads v4 positions by token id in a fixed number of rounds.
func LoadV4Positions(ctx context.Context, c Caller, d Deployment, ids []*big.Int) ([]Position, error) {
	if len(ids) == 0 {
		return []Position{}, nil
	}
	reads := make([]read, 0, len(ids)*3)
	for _, id := range ids {
		reads = append(reads, read{d.Manager, v4, "getPoolAndPositionInfo", []any{id}}, read{d.Manager, v4, "getPositionLiquidity", []any{id}}, read{d.Manager, v4, "ownerOf", []any{id}})
	}
	res, err := batch(ctx, c, d.ChainID, reads)
	if err != nil {
		return nil, err
	}
	type raw struct {
		key       PoolKey
		rg        Range
		liquidity *big.Int
		owner     common.Address
	}
	raws := make([]raw, len(ids))
	state := make([]read, 0, len(ids)*3)
	for i, id := range ids {
		var key PoolKey
		if err := abiCopy(res[i*3][0], &key); err != nil {
			return nil, err
		}
		info := asBig(res[i*3][1])
		if info == nil {
			return nil, errors.New("invalid v4 position info")
		}
		r := raw{key: key, rg: Range{int24At(info, 8), int24At(info, 32)}, liquidity: asBig(res[i*3+1][0]), owner: res[i*3+2][0].(common.Address)}
		raws[i] = r
		pid := key.PoolID()
		salt := common.BigToHash(id)
		state = append(state,
			read{d.StateView, v4, "getSlot0", []any{pid}},
			read{d.StateView, v4, "getFeeGrowthInside", []any{pid, big.NewInt(int64(r.rg.Lower)), big.NewInt(int64(r.rg.Upper))}},
			read{d.StateView, v4, "getPositionInfo", []any{pid, d.Manager, big.NewInt(int64(r.rg.Lower)), big.NewInt(int64(r.rg.Upper)), salt}})
	}
	st, err := batch(ctx, c, d.ChainID, state)
	if err != nil {
		return nil, err
	}
	out := make([]Position, len(ids))
	for i, r := range raws {
		slot, inside, info := st[i*3], st[i*3+1], st[i*3+2]
		tick, err := asInt(slot[1])
		if err != nil {
			return nil, err
		}
		fees := UncollectedFees(asBig(info[0]), asBig(inside[0]), asBig(inside[1]), asBig(info[1]), asBig(info[2]))
		hooks := ""
		if r.key.Hooks != (common.Address{}) {
			hooks = r.key.Hooks.Hex()
		}
		pool := Pool{Version: V4, ChainID: d.ChainID, PoolID: r.key.PoolID().Hex(), Token0: Token{Address: r.key.Currency0.Hex()}, Token1: Token{Address: r.key.Currency1.Hex()},
			Fee: int(r.key.Fee.Int64()), TickSpacing: int(r.key.TickSpacing.Int64()), Hooks: hooks, SqrtPriceX96: asBig(slot[0]), Tick: tick}
		out[i] = Position{TokenID: ids[i].String(), Owner: r.owner.Hex(), Pool: pool, Range: r.rg, Liquidity: r.liquidity, Principal: PositionAmounts(pool.State(), r.rg, r.liquidity), Fees: fees}
	}
	return out, nil
}

func abiCopy(v any, dst *PoolKey) error {
	converted, ok := abi.ConvertType(v, new(PoolKey)).(*PoolKey)
	if !ok || converted == nil {
		return errors.New("invalid v4 pool key")
	}
	*dst = *converted
	return nil
}

// LoadPositions dispatches on the deployment version.
func LoadPositions(ctx context.Context, c Caller, d Deployment, ids []*big.Int) ([]Position, error) {
	if d.Version == V4 {
		return LoadV4Positions(ctx, c, d, ids)
	}
	return LoadV3Positions(ctx, c, d, ids)
}

// TokenMeta reads symbol and decimals for every distinct non-native token in
// one round. Native (address zero) uses nativeSymbol and 18 decimals.
func TokenMeta(ctx context.Context, c Caller, chain uint64, nativeSymbol string, addrs []string) (map[string]Token, error) {
	seen := map[string]bool{}
	var tokens []common.Address
	out := map[string]Token{}
	for _, a := range addrs {
		key := strings.ToLower(a)
		if seen[key] {
			continue
		}
		seen[key] = true
		addr := common.HexToAddress(a)
		if addr == (common.Address{}) {
			out[key] = Token{Address: addr.Hex(), Symbol: nativeSymbol, Decimals: 18}
			continue
		}
		tokens = append(tokens, addr)
	}
	sort.Slice(tokens, func(i, j int) bool { return bytes.Compare(tokens[i].Bytes(), tokens[j].Bytes()) < 0 })
	calls := make([]onchain.Call3, 0, len(tokens)*2)
	for _, t := range tokens {
		calls = append(calls, onchain.Call3{Target: t.Hex(), Data: pack(erc20, "decimals")}, onchain.Call3{Target: t.Hex(), Data: []byte{0x95, 0xd8, 0x9b, 0x41}})
	}
	res, err := c.Aggregate(ctx, chain, calls)
	if err != nil {
		return nil, err
	}
	if len(res) != len(calls) {
		return nil, errors.New("token metadata batch size mismatch")
	}
	for i, t := range tokens {
		dec := res[i*2]
		if !dec.Success || len(dec.Data) != 32 {
			return nil, fmt.Errorf("decimals() failed for %s", t.Hex())
		}
		n := new(big.Int).SetBytes(dec.Data)
		if !n.IsInt64() || n.Int64() > 36 {
			return nil, fmt.Errorf("invalid decimals for %s", t.Hex())
		}
		sym := ""
		if res[i*2+1].Success {
			sym = onchain.DecodeSymbol(res[i*2+1].Data)
		}
		out[strings.ToLower(t.Hex())] = Token{Address: t.Hex(), Symbol: sym, Decimals: int(n.Int64())}
	}
	return out, nil
}

// Fill copies token metadata into a pool.
func (p *Pool) Fill(meta map[string]Token) error {
	t0, ok0 := meta[strings.ToLower(p.Token0.Address)]
	t1, ok1 := meta[strings.ToLower(p.Token1.Address)]
	if !ok0 || !ok1 {
		return errors.New("missing token metadata")
	}
	p.Token0, p.Token1 = t0, t1
	return nil
}

// ReadAllowances reads the approval state of each deposit leg in one round.
func ReadAllowances(ctx context.Context, c Caller, d Deployment, owner common.Address, legs []Allowances) error {
	var reads []read
	for _, l := range legs {
		token := common.HexToAddress(l.Token)
		spender := d.Manager
		if d.Version == V4 {
			spender = d.Permit2
		}
		reads = append(reads, read{token, erc20, "allowance", []any{owner, spender}})
		if d.Version == V4 {
			reads = append(reads, read{d.Permit2, permit2, "allowance", []any{owner, token, d.Manager}})
		}
	}
	if len(reads) == 0 {
		return nil
	}
	res, err := batch(ctx, c, d.ChainID, reads)
	if err != nil {
		return err
	}
	step := 1
	if d.Version == V4 {
		step = 2
	}
	for i := range legs {
		legs[i].ERC20 = asBig(res[i*step][0])
		if d.Version == V4 {
			p := res[i*step+1]
			legs[i].Permit2Amount = asBig(p[0])
			exp := asBig(p[1])
			if exp == nil || !exp.IsUint64() {
				return errors.New("invalid permit2 expiration")
			}
			legs[i].Permit2Expiration = exp.Uint64()
		}
	}
	return nil
}
