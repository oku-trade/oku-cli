package cli

import (
	"fmt"
	"sort"
	"strings"

	urfave "github.com/urfave/cli/v3"
)

var flagEnums = map[string]map[string][]string{
	"orders prepare":     {"side": {"buy", "sell"}, "type": {"limit", "market", "stop_market", "stop_limit", "take_market", "take_limit", "twap", "scale", "trailing_stop"}, "tif": {"gtc", "ioc", "alo"}},
	"orders modify":      {"side": {"buy", "sell"}, "tpsl": {"tp", "sl"}, "tif": {"gtc", "ioc", "alo"}},
	"orders chase":       {"side": {"buy", "sell"}},
	"positions leverage": {"mode": {"cross", "isolated"}},
	"auth login":         {"credential-store": {"file", "keyring"}},
	"auth migrate":       {"to": {"file", "keyring"}},
	"swap orders list":   {"state": {"active", "triggered", "executing", "filled", "failed", "cancelled"}},
	"pnl":                {"range": {"1d", "7d", "30d", "all"}},
}

var requiredFlags = map[string][]string{
	"send prepare":       {"chain", "token", "amount"},
	"send submit":        {"intent", "digest"},
	"send status":        {"intent"},
	"send reconcile":     {"intent", "tx-hash"},
	"swap reconcile":     {"intent", "tx-hash"},
	"deposit prepare":    {"amount"},
	"deposit submit":     {"intent", "digest"},
	"deposit status":     {"intent"},
	"orders prepare":     {"market", "side", "type"},
	"orders submit":      {"intent", "digest"},
	"orders cancel":      {"market"},
	"orders modify":      {"market", "order-id", "side", "size"},
	"orders cancel-twap": {"market", "twap-id"},
	"orders status":      {"intent"},
	"orders abandon":     {"intent"},
	"positions leverage": {"market", "leverage", "mode"},
	"positions margin":   {"market", "amount"},
	"drafts repair":      {"stdin"},
	"swap orders create": {"chain", "token"},
	"swap orders cancel": {"order"},
}

type inputField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  any    `json:"default"`
	Usage    string `json:"usage"`
}

func fields(h *handler) []inputField {
	var out []inputField
	if h.flags == nil {
		return out
	}
	for _, f := range h.flags() {
		x := inputField{Name: f.Names()[0]}
		switch t := f.(type) {
		case *urfave.StringFlag:
			x.Type, x.Default, x.Usage = "string", t.Value, t.Usage
		case *urfave.BoolFlag:
			x.Type, x.Default, x.Usage = "boolean", t.Value, t.Usage
		case *urfave.IntFlag:
			x.Type, x.Default, x.Usage = "integer", t.Value, t.Usage
		case *urfave.Int64Flag:
			x.Type, x.Default, x.Usage = "integer", t.Value, t.Usage
		case *urfave.DurationFlag:
			x.Type, x.Default, x.Usage = "duration", t.Value.String(), t.Usage
		default:
			continue
		}
		x.Required = h.required[x.Name]
		out = append(out, x)
	}
	return out
}

// InputSchema returns the draft 2020-12 schema generated from live command flags.
func InputSchema(path string) (map[string]any, error) {
	h, ok := (&App{}).commandTable()[path]
	if !ok || h.flags == nil {
		return nil, fmt.Errorf("no input schema for %q", path)
	}
	props := map[string]any{}
	required := []string{}
	example := map[string]any{}
	for _, f := range fields(h) {
		t := f.Type
		p := map[string]any{"description": f.Usage, "default": f.Default}
		if t == "duration" {
			t = "string"
			p["pattern"] = `^-?(?:[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h))+$`
		}
		p["type"] = t
		if e := h.enums[f.Name]; len(e) > 0 {
			p["enum"] = e
		}
		props[f.Name] = p
		if f.Required {
			required = append(required, f.Name)
		}
		if f.Required {
			switch f.Type {
			case "integer":
				example[f.Name] = 1
			case "boolean":
				example[f.Name] = true
			default:
				example[f.Name] = "BTC"
			}
			if e := h.enums[f.Name]; len(e) > 0 {
				example[f.Name] = e[0]
			}
			switch f.Name {
			case "size", "amount":
				example[f.Name] = "0.0002"
			case "digest":
				example[f.Name] = strings.Repeat("0", 64)
			case "mode":
				example[f.Name] = "cross"
			case "market":
				example[f.Name] = "BTC"
			case "intent":
				example[f.Name] = "example-intent"
			case "account":
				example[f.Name] = "0x000000000000000000000000000000000000dEaD"
			case "chain":
				example[f.Name] = "42161"
			case "token":
				example[f.Name] = "0x912CE59144191C1204E64559FE8253a0e49E6548"
			case "order":
				example[f.Name] = "so-example"
			}
		}
	}
	if path == "orders prepare" {
		example["price"] = "60000"
		example["size"] = "0.0002"
	}
	if path == "orders chase" {
		example["market"], example["side"], example["size"], example["max-distance"] = "BTC", "buy", "0.0002", "100"
	}
	if path == "orders cancel" {
		example["order-id"] = 1
	}
	if path == "orders modify" {
		example["price"] = "60000"
	}
	if path == "swap orders create" {
		example["all"] = true
		example["stop-loss-pct"] = "10"
	}
	s := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "https://oku.trade/schemas/oku.cli/v1/" + strings.ReplaceAll(path, " ", "-") + ".input.json", "title": "oku " + path + " input", "type": "object", "additionalProperties": false, "properties": props, "examples": []any{example}}
	if len(required) > 0 {
		s["required"] = required
	}
	if path == "orders prepare" {
		s["allOf"] = []any{map[string]any{
			"if":   map[string]any{"properties": map[string]any{"close-position": map[string]any{"const": true}}, "required": []string{"close-position"}},
			"then": map[string]any{"properties": map[string]any{"type": map[string]any{"enum": []string{"stop_market", "stop_limit", "take_market", "take_limit"}}}},
			"else": map[string]any{"required": []string{"size"}},
		}}
	}
	if path == "orders chase" {
		mode := func(required, forbidden []string) map[string]any {
			absent := make([]any, 0, len(forbidden))
			for _, name := range forbidden {
				absent = append(absent, map[string]any{"required": []string{name}})
			}
			return map[string]any{"required": required, "not": map[string]any{"anyOf": absent}}
		}
		prepareOnly := []string{}
		for name := range props {
			if name != "intent" && name != "digest" && name != "resume" {
				prepareOnly = append(prepareOnly, name)
			}
		}
		sort.Strings(prepareOnly)
		s["oneOf"] = []any{
			mode([]string{"market", "side", "size"}, []string{"intent", "digest", "resume"}),
			mode([]string{"intent", "digest"}, append(append([]string{}, prepareOnly...), "resume")),
			mode([]string{"resume"}, append(append([]string{}, prepareOnly...), "intent", "digest")),
		}
	}
	if path == "orders cancel" {
		s["oneOf"] = []any{
			map[string]any{"required": []string{"order-id"}, "not": map[string]any{"required": []string{"order-ids"}}},
			map[string]any{"required": []string{"order-ids"}, "not": map[string]any{"required": []string{"order-id"}}},
		}
	}
	return s, nil
}
