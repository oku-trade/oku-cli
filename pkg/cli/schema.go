package cli

import (
	"fmt"
	"strings"

	urfave "github.com/urfave/cli/v3"
)

var flagEnums = map[string]map[string][]string{
	"orders prepare":     {"side": {"buy", "sell"}, "type": {"limit", "market"}, "tif": {"gtc", "ioc", "alo"}},
	"positions leverage": {"mode": {"cross", "isolated"}},
	"auth login":         {"credential-store": {"file", "keyring"}},
	"auth migrate":       {"to": {"file", "keyring"}},
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
	"orders prepare":     {"market", "side", "type", "size"},
	"orders submit":      {"intent", "digest"},
	"orders cancel":      {"market", "order-id"},
	"orders status":      {"intent"},
	"orders abandon":     {"intent"},
	"positions leverage": {"market", "leverage", "mode"},
	"positions margin":   {"market", "amount"},
	"drafts repair":      {"stdin"},
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
			}
		}
	}
	if path == "orders prepare" {
		example["price"] = "60000"
	}
	s := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "https://oku.trade/schemas/oku.cli/v1/" + strings.ReplaceAll(path, " ", "-") + ".input.json", "title": "oku " + path + " input", "type": "object", "additionalProperties": false, "properties": props, "examples": []any{example}}
	if len(required) > 0 {
		s["required"] = required
	}
	return s, nil
}
