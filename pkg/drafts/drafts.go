// Package drafts turns malformed order-draft JSON (for example model output
// wrapped in code fences or using single quotes) into a strictly validated,
// reviewable candidate. It performs no networking, signing or persistence.
// A candidate is not an intent: it must still go through `orders prepare`,
// which produces the digest a submission is bound to.
//
// Repair is syntax only. It is never applied to backend responses, signed
// actions, MCP frames, credentials, journal records or migration input.
package drafts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kaptinlin/jsonrepair"
	"github.com/oku-trade/oku-cli/pkg/decimal"
)

// Limits on draft input.
const (
	MaxBytes = 16 << 10
	MaxDepth = 4
)

// Order is the strict order-draft schema. Financial values are strings so
// no float conversion ever happens.
type Order struct {
	Market      string `json:"market"`
	Side        string `json:"side"`
	Type        string `json:"type"`
	Size        Dec    `json:"size"`
	Price       Dec    `json:"price,omitempty"`
	TIF         string `json:"tif,omitempty"`
	SlippageBps int    `json:"slippage_bps,omitempty"`
	ReduceOnly  bool   `json:"reduce_only,omitempty"`
}

// Candidate is the review result.
type Candidate struct {
	Repaired bool   `json:"repaired"`
	JSON     string `json:"json"`
	Order    Order  `json:"order"`
	// PrepareArgs is the equivalent `oku orders prepare` argument list. It is
	// informational; nothing is prepared.
	PrepareArgs []string `json:"prepare_args"`
}

// Dec is a decimal taken verbatim from a JSON string or number literal,
// never through float64.
type Dec string

// UnmarshalJSON accepts "1.5" or 1.5 and keeps the exact text.
func (d *Dec) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = Dec(s)
		return nil
	}
	if string(b) == "null" {
		return errors.New("null is not a decimal")
	}
	*d = Dec(b)
	return nil
}

var marketRE = regexp.MustCompile(`^[A-Za-z0-9:_./-]{1,32}$`)

// Repair reads at most MaxBytes of UTF-8 from r and returns a validated
// candidate or an error explaining why the draft is refused.
func Repair(r io.Reader) (*Candidate, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("draft exceeds %d bytes", MaxBytes)
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("draft is not valid UTF-8")
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil, errors.New("empty draft")
	}
	fixed := text
	if !json.Valid([]byte(text)) {
		if fixed, err = jsonrepair.Repair(text); err != nil {
			return nil, fmt.Errorf("draft could not be repaired: %w", err)
		}
	}
	if err := checkShape([]byte(fixed)); err != nil {
		return nil, err
	}
	var o Order
	dec := json.NewDecoder(bytes.NewReader([]byte(fixed)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("draft does not match the order schema: %w", err)
	}
	if dec.More() {
		return nil, errors.New("draft contains more than one JSON value")
	}
	if err := validate(&o); err != nil {
		return nil, err
	}
	if fixed != text {
		// The repair must not have invented or completed a financial value.
		for name, v := range map[string]string{"size": string(o.Size), "price": string(o.Price)} {
			if v != "" && !terminated(text, v) {
				return nil, fmt.Errorf("%s %q appears truncated in the original draft; refusing to guess", name, v)
			}
		}
	}
	return &Candidate{Repaired: fixed != text, JSON: fixed, Order: o, PrepareArgs: prepareArgs(&o)}, nil
}

// checkShape enforces a single flat object, bounded depth and no duplicate
// keys at any level.
func checkShape(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var stack []*frame
	first := true
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid draft JSON: %w", err)
		}
		if first {
			if d, ok := tok.(json.Delim); !ok || d != '{' {
				return errors.New("draft must be a JSON object")
			}
			first = false
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.obj && top.key {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				markValue(stack)
				continue
			}
			k := tok.(string)
			if top.keys[k] {
				return fmt.Errorf("duplicate key %q", k)
			}
			top.keys[k] = true
			top.key = false
			continue
		}
		switch d := tok.(type) {
		case json.Delim:
			switch d {
			case '{', '[':
				stack = append(stack, &frame{obj: d == '{', keys: map[string]bool{}, key: d == '{'})
				if len(stack) > MaxDepth {
					return fmt.Errorf("draft nesting exceeds depth %d", MaxDepth)
				}
			default:
				stack = stack[:len(stack)-1]
				markValue(stack)
			}
		default:
			markValue(stack)
		}
	}
}

type frame struct {
	obj  bool
	keys map[string]bool
	key  bool // next string token is a key
}

func markValue(stack []*frame) {
	if len(stack) > 0 && stack[len(stack)-1].obj {
		stack[len(stack)-1].key = true
	}
}

func validate(o *Order) error {
	if !marketRE.MatchString(o.Market) {
		return fmt.Errorf("market is required (got %q)", o.Market)
	}
	if o.Side != "buy" && o.Side != "sell" {
		return fmt.Errorf("side must be buy or sell (got %q)", o.Side)
	}
	if o.Size == "" {
		return errors.New("size is required; it is never inferred")
	}
	if c, err := decimal.Canonical(string(o.Size)); err != nil || c != string(o.Size) || decimal.IsZero(c) {
		return fmt.Errorf("size %q is not a positive canonical decimal", o.Size)
	}
	switch o.Type {
	case "limit":
		if o.Price == "" {
			return errors.New("price is required for limit orders; it is never inferred")
		}
		if c, err := decimal.Canonical(string(o.Price)); err != nil || c != string(o.Price) || decimal.IsZero(c) {
			return fmt.Errorf("price %q is not a positive canonical decimal", o.Price)
		}
		if o.SlippageBps != 0 {
			return errors.New("slippage_bps applies to market orders only")
		}
		switch strings.ToLower(o.TIF) {
		case "", "gtc", "ioc", "alo":
		default:
			return fmt.Errorf("tif must be gtc, ioc or alo (got %q)", o.TIF)
		}
	case "market":
		if o.Price != "" || o.TIF != "" {
			return errors.New("price and tif are not allowed for market orders")
		}
		if o.SlippageBps <= 0 || o.SlippageBps > 1000 {
			return errors.New("slippage_bps (1-1000) is required for market orders; it is never inferred")
		}
	default:
		return fmt.Errorf("type must be limit or market (got %q)", o.Type)
	}
	return nil
}

// terminated reports whether v appears in the original text followed by a
// delimiter, so repair did not complete a cut-off number.
func terminated(text, v string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], v)
		if j < 0 {
			return false
		}
		end := i + j + len(v)
		if end < len(text) && strings.ContainsRune("\"',} \t\r\n", rune(text[end])) {
			return true
		}
		i = end
		if i >= len(text) {
			return false
		}
	}
}

func prepareArgs(o *Order) []string {
	a := []string{"orders", "prepare", "--market", o.Market, "--side", o.Side, "--type", o.Type, "--size", string(o.Size)}
	if o.Type == "limit" {
		a = append(a, "--price", string(o.Price))
		if o.TIF != "" {
			a = append(a, "--tif", strings.ToLower(o.TIF))
		}
	} else {
		a = append(a, "--slippage-bps", fmt.Sprint(o.SlippageBps))
	}
	if o.ReduceOnly {
		a = append(a, "--reduce-only")
	}
	return a
}
