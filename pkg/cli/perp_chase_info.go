package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

// chaseInfo uses the existing public HTTP info endpoint, with no stream or
// credentials. The response limit applies before decoding and redirects fail.
func (e *env) chaseInfo(ctx context.Context, input any, out any) (err error) {
	endpoint, err := config.LoadHyperliquidInfoURL(e.app.Getenv)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chase info HTTP %d", resp.StatusCode)
	}
	const limit = 2 << 20
	raw, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return errors.New("chase info response too large")
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("invalid chase info response: %w", err)
	}
	return nil
}

var errChaseBookUnavailable = errors.New("chase book unavailable")

func (e *env) chaseBook(ctx context.Context, market string) (string, string, error) {
	var book struct {
		Coin   string `json:"coin"`
		Levels [][]struct {
			Px string `json:"px"`
		} `json:"levels"`
	}
	if err := e.chaseInfo(ctx, map[string]any{"type": "l2Book", "coin": market}, &book); err != nil {
		return "", "", err
	}
	if book.Coin != market || len(book.Levels) != 2 {
		return "", "", errors.New("invalid chase book response")
	}
	if len(book.Levels[0]) == 0 || len(book.Levels[1]) == 0 {
		return "", "", errChaseBookUnavailable
	}
	bid, err := chaseDecimal(book.Levels[0][0].Px, false)
	if err != nil {
		return "", "", err
	}
	ask, err := chaseDecimal(book.Levels[1][0].Px, false)
	return bid, ask, err
}

func chaseDecimal(raw string, zero bool) (string, error) {
	value, err := decimal.Canonical(raw)
	if err != nil {
		return "", err
	}
	if !zero && decimal.IsZero(value) {
		return "", errors.New("expected positive chase decimal")
	}
	return value, nil
}

type chaseOrderSnapshot struct{ Status, Remaining, Price string }

func (e *env) chaseOrder(ctx context.Context, in *trading.Intent, oid int64) (chaseOrderSnapshot, error) {
	var out struct {
		Status string `json:"status"`
		Order  struct {
			Status string `json:"status"`
			Order  struct {
				Coin    string `json:"coin"`
				Side    string `json:"side"`
				Oid     int64  `json:"oid"`
				Sz      string `json:"sz"`
				LimitPx string `json:"limitPx"`
			} `json:"order"`
		} `json:"order"`
	}
	if oid <= 0 {
		return chaseOrderSnapshot{}, errors.New("chase has no owned order")
	}
	if err := e.chaseInfo(ctx, map[string]any{"type": "orderStatus", "user": in.Account, "oid": oid}, &out); err != nil {
		return chaseOrderSnapshot{}, err
	}
	o := out.Order.Order
	side := "A"
	if in.IsBuy() {
		side = "B"
	}
	if out.Status != "order" || o.Oid != oid || o.Coin != in.Market || o.Side != side {
		return chaseOrderSnapshot{}, errors.New("chase order status unavailable or mismatched")
	}
	remaining, err := chaseDecimal(o.Sz, true)
	if err != nil {
		return chaseOrderSnapshot{}, err
	}
	price, err := chaseDecimal(o.LimitPx, false)
	if err != nil {
		return chaseOrderSnapshot{}, err
	}
	if decimal.Decimals(remaining) > in.Chase.SizeDecimals {
		return chaseOrderSnapshot{}, errors.New("invalid chase remaining precision")
	}
	return chaseOrderSnapshot{Status: out.Order.Status, Remaining: remaining, Price: price}, nil
}

// chaseFillPageLimit is the venue's page size for userFillsByTime. A full page
// may be truncated, so the next request resumes at its last timestamp.
const chaseFillPageLimit = 2000

// chaseFillMaxPages bounds the history walk. Exceeding it fails closed.
const chaseFillMaxPages = 25

type chaseFill struct {
	Oid  int64  `json:"oid"`
	Tid  int64  `json:"tid"`
	Time int64  `json:"time"`
	Sz   string `json:"sz"`
	Px   string `json:"px"`
	Coin string `json:"coin"`
	Side string `json:"side"`
}

// chaseFillHistory pages the account's fills since the parent was created.
// Overlapping boundary fills are deduplicated by trade id by the caller.
func (e *env) chaseFillHistory(ctx context.Context, in *trading.Intent) ([]chaseFill, error) {
	start, err := time.Parse(time.RFC3339, in.CreatedAt)
	if err != nil {
		return nil, err
	}
	cursor := start.UnixMilli()
	var all []chaseFill
	for range chaseFillMaxPages {
		var page []chaseFill
		if err = e.chaseInfo(ctx, map[string]any{"type": "userFillsByTime", "user": in.Account, "startTime": cursor, "aggregateByTime": false}, &page); err != nil {
			return nil, err
		}
		if page == nil {
			return nil, errors.New("chase fills missing")
		}
		all = append(all, page...)
		if len(page) < chaseFillPageLimit {
			return all, nil
		}
		last := page[len(page)-1].Time
		if last <= cursor {
			return nil, errors.New("chase fill history did not advance")
		}
		cursor = last
	}
	return nil, errors.New("chase fill history exceeds the page bound")
}

// chaseFills only counts fills belonging to durably owned order ids.
func (e *env) chaseFills(ctx context.Context, in *trading.Intent, c *journal.ChaseRecord) (string, *string, error) {
	fills, err := e.chaseFillHistory(ctx, in)
	if err != nil {
		return "", nil, err
	}
	owned := map[int64]bool{}
	for _, oid := range c.OrderIDs {
		owned[oid] = true
	}
	seen := map[int64]bool{}
	size, cost := new(big.Rat), new(big.Rat)
	side := "A"
	if in.IsBuy() {
		side = "B"
	}
	for _, f := range fills {
		if !owned[f.Oid] {
			continue
		}
		if f.Tid <= 0 || f.Coin != in.Market || f.Side != side {
			return "", nil, errors.New("invalid owned chase fill")
		}
		if seen[f.Tid] {
			continue
		}
		seen[f.Tid] = true
		s, err := chaseDecimal(f.Sz, false)
		if err != nil {
			return "", nil, err
		}
		if err = decimal.ValidateSize(s, in.Chase.SizeDecimals); err != nil {
			return "", nil, err
		}
		p, err := chaseDecimal(f.Px, false)
		if err != nil {
			return "", nil, err
		}
		sr, _ := decimal.Rat(s)
		pr, _ := decimal.Rat(p)
		size.Add(size, sr)
		cost.Add(cost, new(big.Rat).Mul(sr, pr))
	}
	total, _ := decimal.Rat(in.Size)
	if total == nil || size.Cmp(total) > 0 {
		return "", nil, errors.New("chase fills exceed approved size")
	}
	filled, err := decimal.Canonical(size.FloatString(in.Chase.SizeDecimals))
	if err != nil {
		return "", nil, err
	}
	if size.Sign() == 0 {
		return filled, nil, nil
	}
	average, err := decimal.Canonical(new(big.Rat).Quo(cost, size).FloatString(18))
	if err != nil {
		return "", nil, err
	}
	return filled, &average, nil
}

func chaseSubtract(a, b string) (string, error) {
	ar, aok := decimal.Rat(a)
	br, bok := decimal.Rat(b)
	if !aok || !bok || ar.Cmp(br) < 0 {
		return "", errors.New("invalid chase remaining budget")
	}
	return decimal.Canonical(new(big.Rat).Sub(ar, br).FloatString(18))
}
