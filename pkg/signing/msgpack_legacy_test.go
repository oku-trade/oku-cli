package signing

// Legacy handwritten encoder, kept only as a differential oracle for the
// vmihailenco/msgpack-backed EncodeActionJSON.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// maxSafeInteger mirrors JavaScript Number.MAX_SAFE_INTEGER. The reference
// frontend parses action_json with JSON.parse, so integers outside this range
// would be silently rounded there. We refuse them instead of guessing.
const legacyMaxSafeInteger = 1<<53 - 1

// EncodeActionJSON msgpack-encodes a JSON document preserving object key
// order, matching @msgpack/msgpack encode(JSON.parse(actionJSON)) as used by
// trade/src/app/features/perps/lib/hyperliquidHash.ts. Non-integer numbers are
// rejected (fail closed): no Oku action currently contains them and float
// encoding differs across SDKs.
func legacyEncodeActionJSON(actionJSON string) ([]byte, error) {
	dec := json.NewDecoder(strings.NewReader(actionJSON))
	dec.UseNumber()
	var buf bytes.Buffer
	if err := legacyEncodeValue(dec, &buf); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("action json has trailing data")
	}
	return buf.Bytes(), nil
}

func legacyEncodeValue(dec *json.Decoder, buf *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("parse action json: %w", err)
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return legacyEncodeObject(dec, buf)
		case '[':
			return legacyEncodeArray(dec, buf)
		}
		return fmt.Errorf("unexpected delimiter %q", v)
	case string:
		legacyWriteString(buf, v)
	case bool:
		if v {
			buf.WriteByte(0xc3)
		} else {
			buf.WriteByte(0xc2)
		}
	case nil:
		buf.WriteByte(0xc0)
	case json.Number:
		return legacyWriteNumber(buf, v)
	default:
		return fmt.Errorf("unsupported json token %T", tok)
	}
	return nil
}

func legacyEncodeObject(dec *json.Decoder, buf *bytes.Buffer) error {
	var body bytes.Buffer
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
		if legacyIsArrayIndexKey(key) {
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
		legacyWriteString(&body, key)
		if err := legacyEncodeValue(dec, &body); err != nil {
			return err
		}
		n++
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	legacyWriteHeader(buf, n, 0x80, 0xde, 0xdf)
	buf.Write(body.Bytes())
	return nil
}

func legacyEncodeArray(dec *json.Decoder, buf *bytes.Buffer) error {
	var body bytes.Buffer
	n := 0
	for dec.More() {
		if err := legacyEncodeValue(dec, &body); err != nil {
			return err
		}
		n++
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	legacyWriteHeader(buf, n, 0x90, 0xdc, 0xdd)
	buf.Write(body.Bytes())
	return nil
}

func legacyWriteHeader(buf *bytes.Buffer, n int, fix, b16, b32 byte) {
	switch {
	case n < 16:
		buf.WriteByte(fix | byte(n))
	case n < 1<<16:
		buf.WriteByte(b16)
		_ = binary.Write(buf, binary.BigEndian, uint16(n))
	default:
		buf.WriteByte(b32)
		_ = binary.Write(buf, binary.BigEndian, uint32(n))
	}
}

func legacyWriteString(buf *bytes.Buffer, s string) {
	n := len(s)
	switch {
	case n < 32:
		buf.WriteByte(0xa0 | byte(n))
	case n < 1<<8:
		buf.WriteByte(0xd9)
		buf.WriteByte(byte(n))
	case n < 1<<16:
		buf.WriteByte(0xda)
		_ = binary.Write(buf, binary.BigEndian, uint16(n))
	default:
		buf.WriteByte(0xdb)
		_ = binary.Write(buf, binary.BigEndian, uint32(n))
	}
	buf.WriteString(s)
}

func legacyWriteNumber(buf *bytes.Buffer, n json.Number) error {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return fmt.Errorf("non-integer number %s in action json is not supported", s)
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil || i > legacyMaxSafeInteger || i < -legacyMaxSafeInteger {
		return fmt.Errorf("integer %s outside JavaScript safe range", s)
	}
	if i >= 0 {
		u := uint64(i)
		switch {
		case u < 0x80:
			buf.WriteByte(byte(u))
		case u <= math.MaxUint8:
			buf.Write([]byte{0xcc, byte(u)})
		case u <= math.MaxUint16:
			buf.WriteByte(0xcd)
			_ = binary.Write(buf, binary.BigEndian, uint16(u))
		case u <= math.MaxUint32:
			buf.WriteByte(0xce)
			_ = binary.Write(buf, binary.BigEndian, uint32(u))
		default:
			buf.WriteByte(0xcf)
			_ = binary.Write(buf, binary.BigEndian, u)
		}
		return nil
	}
	switch {
	case i >= -0x20:
		buf.WriteByte(byte(int8(i)))
	case i >= math.MinInt8:
		buf.Write([]byte{0xd0, byte(int8(i))})
	case i >= math.MinInt16:
		buf.WriteByte(0xd1)
		_ = binary.Write(buf, binary.BigEndian, int16(i))
	case i >= math.MinInt32:
		buf.WriteByte(0xd2)
		_ = binary.Write(buf, binary.BigEndian, int32(i))
	default:
		buf.WriteByte(0xd3)
		_ = binary.Write(buf, binary.BigEndian, i)
	}
	return nil
}

// isArrayIndexKey reports whether JS would treat k as an array index
// (canonical uint32 below 2^32-1), which changes property order.
func legacyIsArrayIndexKey(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 32)
	return err == nil && n < math.MaxUint32
}
