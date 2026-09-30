package signing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/vmihailenco/msgpack/v5"
)

// maxSafeInteger mirrors JavaScript Number.MAX_SAFE_INTEGER. The reference
// frontend parses action_json with JSON.parse, so integers outside this range
// would be silently rounded there. We refuse them instead of guessing.
const maxSafeInteger = 1<<53 - 1

// EncodeActionJSON msgpack-encodes a JSON document preserving object key
// order, matching @msgpack/msgpack encode(JSON.parse(actionJSON)) as used by
// trade/src/app/features/perps/lib/hyperliquidHash.ts. The JSON is walked
// token by token so key order is exactly the wire order; the msgpack wire
// primitives come from github.com/vmihailenco/msgpack/v5, the library the
// account backend uses for the same hash. Non-integer numbers are rejected
// (fail closed): no Oku action currently contains them and float encoding
// differs across SDKs.
func EncodeActionJSON(actionJSON string) ([]byte, error) {
	dec := json.NewDecoder(strings.NewReader(actionJSON))
	dec.UseNumber()
	var buf bytes.Buffer
	if err := encodeValue(dec, &buf); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("action json has trailing data")
	}
	return buf.Bytes(), nil
}

func newEncoder(w io.Writer) *msgpack.Encoder {
	enc := msgpack.NewEncoder(w)
	// Smallest integer representation, as @msgpack/msgpack does.
	enc.UseCompactInts(true)
	return enc
}

func encodeValue(dec *json.Decoder, buf *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("parse action json: %w", err)
	}
	enc := newEncoder(buf)
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return encodeObject(dec, buf)
		case '[':
			return encodeArray(dec, buf)
		}
		return fmt.Errorf("unexpected delimiter %q", v)
	case string:
		return enc.EncodeString(v)
	case bool:
		return enc.EncodeBool(v)
	case nil:
		return enc.EncodeNil()
	case json.Number:
		i, err := safeInt(v)
		if err != nil {
			return err
		}
		return enc.EncodeInt(i)
	}
	return fmt.Errorf("unsupported json token %T", tok)
}

func encodeObject(dec *json.Decoder, buf *bytes.Buffer) error {
	var body bytes.Buffer
	enc := newEncoder(&body)
	n := 0
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if isArrayIndexKey(key) {
			// JavaScript objects enumerate integer-like keys first, so
			// JSON.parse would reorder them relative to the wire bytes.
			return fmt.Errorf("integer-like key %q in action json is not supported", key)
		}
		if seen[key] {
			// JSON.parse keeps the last value at the first position; refuse
			// ambiguous payloads rather than reproduce that quirk.
			return fmt.Errorf("duplicate key %q in action json", key)
		}
		seen[key] = true
		if err := enc.EncodeString(key); err != nil {
			return err
		}
		if err := encodeValue(dec, &body); err != nil {
			return err
		}
		n++
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if err := newEncoder(buf).EncodeMapLen(n); err != nil {
		return err
	}
	buf.Write(body.Bytes())
	return nil
}

func encodeArray(dec *json.Decoder, buf *bytes.Buffer) error {
	var body bytes.Buffer
	n := 0
	for dec.More() {
		if err := encodeValue(dec, &body); err != nil {
			return err
		}
		n++
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if err := newEncoder(buf).EncodeArrayLen(n); err != nil {
		return err
	}
	buf.Write(body.Bytes())
	return nil
}

func safeInt(n json.Number) (int64, error) {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return 0, fmt.Errorf("non-integer number %s in action json is not supported", s)
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil || i > maxSafeInteger || i < -maxSafeInteger {
		return 0, fmt.Errorf("integer %s outside JavaScript safe range", s)
	}
	return i, nil
}

// isArrayIndexKey reports whether JS would treat k as an array index
// (canonical uint32 below 2^32-1), which changes property order.
func isArrayIndexKey(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 32)
	return err == nil && n < math.MaxUint32
}
