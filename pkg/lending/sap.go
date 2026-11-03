package lending

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// SAP is the Morpho indexer the Oku web app reads vaults, markets, reward
// campaigns and a user's active positions from (JSON-RPC over HTTPS).
type SAP struct {
	URL       string
	Client    *http.Client
	UserAgent string
}

// SAPVault is one blue/getVaults entry. Numeric fields stay raw strings and
// are parsed where used.
type SAPVault struct {
	Snapshot struct {
		Address         string   `json:"address"`
		ChainID         uint64   `json:"chain_id"`
		Token           string   `json:"token"`
		TokenDecimals   *int     `json:"token_decimals"`
		Version         int      `json:"version"`
		LastTotalAssets string   `json:"last_total_assets"`
		Fee             string   `json:"fee"`
		Name            string   `json:"name"`
		Curator         string   `json:"curator"`
		SupplyQueue     []string `json:"supply_queue"`
	} `json:"snapshot"`
	Calculated struct {
		VaultSupplyAPY          string `json:"vault_supply_apy"`
		VaultSupplyNetAPY       string `json:"vault_supply_net_apy"`
		AvailableLiquidity      string `json:"available_liquidity"`
		AnnualizedManagementFee string `json:"annualized_management_fee"`
	} `json:"calculated"`
}

// SAPMarket is one blue/getMarkets entry.
type SAPMarket struct {
	Snapshot struct {
		MarketID          string `json:"market_id"`
		ChainID           uint64 `json:"chain_id"`
		LoanToken         string `json:"loan_token"`
		CollateralToken   string `json:"collateral_token"`
		Oracle            string `json:"oracle_address"`
		Irm               string `json:"irm_address"`
		LLTV              string `json:"lltv"`
		OraclePrice       string `json:"oracle_price"`
		TotalSupplyAssets string `json:"total_supply_assets"`
		TotalBorrowAssets string `json:"total_borrow_assets"`
	} `json:"snapshot"`
	Calculated struct {
		Utilization string `json:"utilization"`
		SupplyAPY   string `json:"supply_apy"`
		BorrowAPY   string `json:"borrow_apy"`
	} `json:"calculated"`
}

type campaignSpec struct {
	ChainID uint64 `json:"chainId"`
	Start   int64  `json:"start_timestamp"`
	End     int64  `json:"end_timestamp"`
}

type sapProgram struct {
	Spec campaignSpec `json:"spec"`
	APY  float64      `json:"apy"`
}

// MerklOpportunity is the subset of a live Merkl opportunity the web app
// reads to adjust APYs.
type MerklOpportunity struct {
	Identifier string  `json:"identifier"`
	ChainID    uint64  `json:"chainId"`
	Status     string  `json:"status"`
	APR        float64 `json:"apr"` // percent
	Type       string  `json:"type"`
	Campaigns  []struct {
		Start  int64 `json:"startTimestamp"`
		End    int64 `json:"endTimestamp"`
		Params *struct {
			Method *struct {
				Method   string `json:"distributionMethod"`
				Settings *struct {
					Mode      string `json:"mode"`
					TargetAPR string `json:"targetAPR"`
				} `json:"distributionSettings"`
			} `json:"distributionMethodParameters"`
		} `json:"params"`
	} `json:"campaigns"`
}

// Campaigns is blue/getLiveCampaigns: Morpho reward programs keyed by vault
// address and market id, plus live Merkl opportunities.
type Campaigns struct {
	Markets map[string][]sapProgram `json:"markets"`
	Vaults  map[string][]sapProgram `json:"vaults"`
	Merkl   []MerklOpportunity      `json:"merkl"`
}

// ActiveVault and ActiveMarket are a user's indexed Morpho positions.
type ActiveVault struct {
	Address string `json:"VaultAddress"`
	ChainID uint64 `json:"ChainID"`
}
type ActiveMarket struct {
	MarketID string `json:"MarketID"`
	ChainID  uint64 `json:"ChainID"`
}

func (s SAP) call(ctx context.Context, method string, params []any, out any) error {
	if s.URL == "" {
		return errors.New("morpho indexer URL is not configured")
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("morpho indexer %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("morpho indexer %s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("morpho indexer %s: HTTP %d", method, resp.StatusCode)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("morpho indexer %s: %w", method, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("morpho indexer %s: %s", method, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("morpho indexer %s: empty result", method)
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("morpho indexer %s: %w", method, err)
	}
	return nil
}

func chainFilter(chain uint64) []any {
	return []any{map[string]any{"filter": map[string]any{"chain_ids": []uint64{chain}}}}
}

// Vaults lists a chain's MetaMorpho (V1) and Morpho Vault V2 vaults.
func (s SAP) Vaults(ctx context.Context, chain uint64) ([]SAPVault, error) {
	var out []SAPVault
	return out, s.call(ctx, "blue/getVaults", chainFilter(chain), &out)
}

// Markets lists a chain's Morpho Blue markets.
func (s SAP) Markets(ctx context.Context, chain uint64) ([]SAPMarket, error) {
	var out []SAPMarket
	return out, s.call(ctx, "blue/getMarkets", chainFilter(chain), &out)
}

// LiveCampaigns reads every live reward program.
func (s SAP) LiveCampaigns(ctx context.Context) (*Campaigns, error) {
	var out Campaigns
	return &out, s.call(ctx, "blue/getLiveCampaigns", []any{}, &out)
}

// ActiveVaults lists vaults the user holds shares in, on any chain.
func (s SAP) ActiveVaults(ctx context.Context, user string) ([]ActiveVault, error) {
	var out struct {
		Vaults []ActiveVault `json:"vaults"`
	}
	return out.Vaults, s.call(ctx, "blue/getActiveVaultsForUser", []any{user}, &out)
}

// ActiveMarkets lists markets the user has a position in, on any chain.
func (s SAP) ActiveMarkets(ctx context.Context, user string) ([]ActiveMarket, error) {
	var out struct {
		Markets []ActiveMarket `json:"markets"`
	}
	return out.Markets, s.call(ctx, "blue/getActiveMarketsForUser", []any{user}, &out)
}

// WadFloat parses a WAD-scaled decimal string (1e18 = 1.0). ok is false for
// an empty or malformed value so callers report N/A, never zero.
func WadFloat(s string) (float64, bool) {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return 0, false
	}
	return Float(v, 18), true
}

// VaultAPY is a vault's yield the way the web app shows it: base net of
// fees, plus live Morpho reward programs and a live Merkl opportunity.
type VaultAPY struct {
	Gross   float64  `json:"gross_apy"`
	Base    float64  `json:"base_apy"`
	Rewards float64  `json:"rewards_apy"`
	Net     float64  `json:"net_apy"`
	Sources []string `json:"reward_sources,omitempty"`
}

// VaultYield mirrors the web app's vault APY: base = vault_supply_net_apy,
// net = base + active Morpho programs + Merkl APR (FIX_APR campaigns pay
// the gap to their target only).
func VaultYield(v SAPVault, c *Campaigns, now int64) (*VaultAPY, error) {
	base, ok := WadFloat(v.Calculated.VaultSupplyNetAPY)
	if !ok {
		return nil, errors.New("vault net APY unavailable")
	}
	gross, ok := WadFloat(v.Calculated.VaultSupplyAPY)
	if !ok {
		return nil, errors.New("vault gross APY unavailable")
	}
	out := &VaultAPY{Gross: gross, Base: base, Net: base}
	if c == nil {
		return out, nil
	}
	for _, p := range c.Vaults[v.Snapshot.Address] {
		if p.Spec.ChainID == v.Snapshot.ChainID && p.Spec.Start <= now && p.Spec.End >= now {
			out.Rewards += p.APY
			out.Sources = append(out.Sources, "morpho")
		}
	}
	for _, m := range c.Merkl {
		if m.ChainID == v.Snapshot.ChainID && m.Status == "LIVE" && strings.EqualFold(m.Identifier, v.Snapshot.Address) {
			apr := m.APR
			if target, fixed := fixAPRTarget(m, now); fixed {
				apr = max((target-base)*100, 0)
			}
			if apr > 0 {
				out.Rewards += apr / 100
				out.Sources = append(out.Sources, "merkl")
			}
			break
		}
	}
	out.Net = base + out.Rewards
	return out, nil
}

// fixAPRTarget mirrors fixAprTargetApr: the highest live ERC4626_APR
// FIX_APR target, as a fraction.
func fixAPRTarget(m MerklOpportunity, now int64) (float64, bool) {
	best, found := 0.0, false
	for _, c := range m.Campaigns {
		if c.Start > now || c.End < now || c.Params == nil || c.Params.Method == nil || c.Params.Method.Settings == nil {
			continue
		}
		if c.Params.Method.Method != "ERC4626_APR" || c.Params.Method.Settings.Mode != "FIX_APR" {
			continue
		}
		var t float64
		if _, err := fmt.Sscan(c.Params.Method.Settings.TargetAPR, &t); err == nil && t > 0 && (!found || t > best) {
			best, found = t, true
		}
	}
	return best, found
}

// BorrowRate is a Morpho market's borrow cost net of borrow rewards.
type BorrowRate struct {
	Borrow  float64 `json:"borrow_apy"`
	Rewards float64 `json:"borrow_rewards_apy"`
	Net     float64 `json:"net_borrow_apy"`
}

// MarketBorrowRate mirrors the web app's market netApr: borrow APY minus
// active Morpho programs and a live Merkl opportunity whose identifier
// prefixes the market id.
func MarketBorrowRate(m SAPMarket, c *Campaigns, now int64) (*BorrowRate, error) {
	borrow, ok := WadFloat(m.Calculated.BorrowAPY)
	if !ok {
		return nil, errors.New("market borrow APY unavailable")
	}
	out := &BorrowRate{Borrow: borrow}
	if c != nil {
		for _, p := range c.Markets[m.Snapshot.MarketID] {
			if p.Spec.ChainID == m.Snapshot.ChainID && p.Spec.Start <= now && p.Spec.End >= now {
				out.Rewards += p.APY
			}
		}
		id := strings.ToLower(m.Snapshot.MarketID)
		for _, o := range c.Merkl {
			ident := strings.ToLower(o.Identifier)
			if o.ChainID == m.Snapshot.ChainID && o.Status == "LIVE" && len(ident) > 1 && strings.HasPrefix(id, ident[:len(ident)-1]) {
				if o.APR > 0 {
					out.Rewards += o.APR / 100
				}
				break
			}
		}
	}
	out.Net = out.Borrow - out.Rewards
	return out, nil
}
