package trading

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// shape is an exact JSON schema node. encoding/json matches struct fields
// case-insensitively, so struct decoding alone accepts aliases such as "S"
// for "s" (the later one wins) while the signer preserves both keys. Every
// action is therefore checked against an exact, case-sensitive shape on the
// same bytes that are signed before any struct decoding.
type shape struct {
	kind     string // "object", "array", "string", "number", "bool", "any"
	fields   map[string]field
	elem     *shape
	nonEmpty bool
}

type field struct {
	s        *shape
	optional bool
}

var (
	sString = &shape{kind: "string"}
	sNumber = &shape{kind: "number"}
	sBool   = &shape{kind: "bool"}
	sAny    = &shape{kind: "any"}
)

func obj(fields map[string]field) *shape { return &shape{kind: "object", fields: fields} }
func req(s *shape) field                 { return field{s: s} }
func opt(s *shape) field                 { return field{s: s, optional: true} }

var actionShapes = map[string]*shape{
	"order": obj(map[string]field{
		"type": req(sString),
		"orders": req(&shape{kind: "array", elem: obj(map[string]field{
			"a": req(sNumber), "b": req(sBool), "p": req(sString), "s": req(sString), "r": req(sBool),
			"t": req(obj(map[string]field{
				"limit":   opt(obj(map[string]field{"tif": req(sString)})),
				"trigger": opt(sAny),
			})),
			"c": opt(sString),
		})}),
		"grouping": req(sString),
		"builder":  opt(obj(map[string]field{"b": req(sString), "f": req(sNumber)})),
	}),
	"cancel": obj(map[string]field{
		"type":    req(sString),
		"cancels": req(&shape{kind: "array", elem: obj(map[string]field{"a": req(sNumber), "o": req(sNumber)})}),
	}),
	"updateLeverage": obj(map[string]field{
		"type": req(sString), "asset": req(sNumber), "isCross": req(sBool), "leverage": req(sNumber),
	}),
	"updateIsolatedMargin": obj(map[string]field{
		"type": req(sString), "asset": req(sNumber), "isBuy": req(sBool), "ntli": req(sNumber),
	}),
}

// checkExactShape verifies actionJSON against the shape for actionType with
// case-sensitive keys, no duplicates, no unknown or missing required keys,
// no nulls and exact value kinds.
func checkExactShape(actionType, actionJSON string) error {
	s, ok := actionShapes[actionType]
	if !ok {
		return mismatch("unsupported action type %q", actionType)
	}
	dec := json.NewDecoder(strings.NewReader(actionJSON))
	dec.UseNumber()
	if err := walk(dec, s, "$"); err != nil {
		return mismatch("unexpected action shape: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return mismatch("trailing data in action")
	}
	return nil
}

func walk(dec *json.Decoder, s *shape, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	if s.kind == "any" {
		return skip(dec, tok)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch {
		case t == '{' && s.kind == "object":
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return fmt.Errorf("%s: %v", path, err)
				}
				k := kt.(string)
				if seen[k] {
					return fmt.Errorf("%s: duplicate key %q", path, k)
				}
				seen[k] = true
				f, ok := s.fields[k]
				if !ok {
					return fmt.Errorf("%s: unknown key %q", path, k)
				}
				if err := walk(dec, f.s, path+"."+k); err != nil {
					return err
				}
			}
			for k, f := range s.fields {
				if !f.optional && !seen[k] {
					return fmt.Errorf("%s: missing key %q", path, k)
				}
			}
			_, err := dec.Token()
			return err
		case t == '[' && s.kind == "array":
			for i := 0; dec.More(); i++ {
				if err := walk(dec, s.elem, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		}
	case string:
		if s.kind == "string" {
			return nil
		}
	case json.Number:
		if s.kind == "number" {
			return nil
		}
	case bool:
		if s.kind == "bool" {
			return nil
		}
	}
	return fmt.Errorf("%s: expected %s", path, s.kind)
}

func skip(dec *json.Decoder, tok json.Token) error {
	d, ok := tok.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return nil
	}
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}
