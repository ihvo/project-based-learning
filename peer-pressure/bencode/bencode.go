// Package bencode implements encoding and decoding of the BitTorrent bencode format.
//
// Bencode supports four types:
//   - Byte strings: length-prefixed binary data
//   - Integers: arbitrary-precision signed integers
//   - Lists: ordered sequences of values
//   - Dictionaries: string-keyed maps with lexicographically sorted keys
//
// Reference: BEP 3 — https://www.bittorrent.org/beps/bep_0003.html
package bencode

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Value represents a bencoded value. We use an interface so any of the four
// bencode types can be stored in lists and dicts without boxing gymnastics.
//
// The concrete types are:
//   - String (wraps []byte — bencode "strings" are really byte strings)
//   - Int    (wraps int64)
//   - List   (wraps []Value)
//   - Dict   (wraps map[string]Value — keys are always strings per spec)
type Value interface {
	bencode() // marker method — prevents outside types from satisfying the interface
}

// String is a bencode byte string. Named "String" because that's what the spec
// calls it, but it holds raw bytes — it may not be valid UTF-8.
type String []byte

func (String) bencode() {}

// Int is a bencode integer.
type Int int64

func (Int) bencode() {}

// List is an ordered sequence of bencode values.
type List []Value

func (List) bencode() {}

// Dict is a bencode dictionary. Keys are strings, sorted lexicographically
// when encoded to guarantee deterministic output.
type Dict map[string]Value

func (Dict) bencode() {}

// --- Encoding ---

// Encode serializes a Value into its bencoded byte representation.
// Dict keys are always sorted lexicographically (per BEP 3).
func Encode(v Value) []byte {
	var buf []byte
	return appendEncoded(buf, v)
}

func appendEncoded(buf []byte, v Value) []byte {
	switch v := v.(type) {
	case String:
		// Format: <length>:<data>
		buf = strconv.AppendInt(buf, int64(len(v)), 10)
		buf = append(buf, ':')
		buf = append(buf, v...)

	case Int:
		// Format: i<number>e
		buf = append(buf, 'i')
		buf = strconv.AppendInt(buf, int64(v), 10)
		buf = append(buf, 'e')

	case List:
		// Format: l<item1><item2>...e
		buf = append(buf, 'l')
		for _, item := range v {
			buf = appendEncoded(buf, item)
		}
		buf = append(buf, 'e')

	case Dict:
		// Format: d<key1><val1><key2><val2>...e  (keys sorted)
		buf = append(buf, 'd')
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			buf = appendEncoded(buf, String(k))
			buf = appendEncoded(buf, v[k])
		}
		buf = append(buf, 'e')
	}
	return buf
}

// --- Decoding ---

var (
	ErrUnexpectedEnd = errors.New("bencode: unexpected end of input")
	ErrInvalidFormat = errors.New("bencode: invalid format")
)

// Decode parses a bencoded byte slice and returns the decoded Value.
// Returns an error if the input is malformed.
func Decode(data []byte) (Value, error) {
	val, rest, err := decodeValue(data)
	if err != nil {
		return nil, err
	}
	if len(rest) > 0 {
		return nil, fmt.Errorf("%w: trailing data after value", ErrInvalidFormat)
	}
	return val, nil
}

// decodeValue parses one value from the front of data, returning the value
// and the remaining unconsumed bytes. This "return the rest" pattern lets us
// recursively parse nested structures without tracking an index.
func decodeValue(data []byte) (Value, []byte, error) {
	if len(data) == 0 {
		return nil, nil, ErrUnexpectedEnd
	}

	switch {
	case data[0] == 'i':
		return decodeInt(data)
	case data[0] == 'l':
		return decodeList(data)
	case data[0] == 'd':
		return decodeDict(data)
	case data[0] >= '0' && data[0] <= '9':
		return decodeString(data)
	default:
		return nil, nil, fmt.Errorf("%w: unexpected byte %q", ErrInvalidFormat, data[0])
	}
}

// decodeString parses: <length>:<data>
func decodeString(data []byte) (String, []byte, error) {
	// Find the colon separating length from content
	colonIdx := -1
	for i, b := range data {
		if b == ':' {
			colonIdx = i
			break
		}
		if b < '0' || b > '9' {
			return nil, nil, fmt.Errorf("%w: non-digit in string length", ErrInvalidFormat)
		}
	}
	if colonIdx == -1 {
		return nil, nil, fmt.Errorf("%w: missing colon in string", ErrInvalidFormat)
	}

	length, err := strconv.ParseInt(string(data[:colonIdx]), 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: bad string length: %v", ErrInvalidFormat, err)
	}

	start := colonIdx + 1
	end := start + int(length)
	if end > len(data) {
		return nil, nil, fmt.Errorf("%w: string length %d exceeds available data", ErrUnexpectedEnd, length)
	}

	// Zero-copy: return a slice into the original input buffer
	return String(data[start:end]), data[end:], nil
}

// decodeInt parses: i<number>e
func decodeInt(data []byte) (Int, []byte, error) {
	if len(data) < 3 { // minimum: "i0e"
		return 0, nil, ErrUnexpectedEnd
	}

	// Find the closing 'e'
	eIdx := -1
	for i := 1; i < len(data); i++ {
		if data[i] == 'e' {
			eIdx = i
			break
		}
	}
	if eIdx == -1 {
		return 0, nil, fmt.Errorf("%w: missing 'e' in integer", ErrInvalidFormat)
	}

	numStr := string(data[1:eIdx])

	// BEP 3: no leading zeros (except "0" itself)
	if len(numStr) > 1 && numStr[0] == '0' {
		return 0, nil, fmt.Errorf("%w: leading zero in integer", ErrInvalidFormat)
	}
	// BEP 3: negative zero is not allowed
	if numStr == "-0" {
		return 0, nil, fmt.Errorf("%w: negative zero in integer", ErrInvalidFormat)
	}

	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: bad integer: %v", ErrInvalidFormat, err)
	}

	return Int(n), data[eIdx+1:], nil
}

// decodeList parses: l<items>e
func decodeList(data []byte) (List, []byte, error) {
	// Skip the 'l'
	data = data[1:]

	var items List
	for {
		if len(data) == 0 {
			return nil, nil, fmt.Errorf("%w: missing 'e' in list", ErrUnexpectedEnd)
		}
		if data[0] == 'e' {
			return items, data[1:], nil
		}
		val, rest, err := decodeValue(data)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, val)
		data = rest
	}
}

// decodeDict parses: d<key><value>...e
func decodeDict(data []byte) (Dict, []byte, error) {
	// Skip the 'd'
	data = data[1:]

	dict := make(Dict)
	var prevKey string
	first := true

	for {
		if len(data) == 0 {
			return nil, nil, fmt.Errorf("%w: missing 'e' in dict", ErrUnexpectedEnd)
		}
		if data[0] == 'e' {
			return dict, data[1:], nil
		}

		// Keys must be strings
		key, rest, err := decodeString(data)
		if err != nil {
			return nil, nil, fmt.Errorf("dict key: %w", err)
		}

		keyStr := string(key)

		// BEP 3: keys must appear in sorted order
		if !first && keyStr <= prevKey {
			return nil, nil, fmt.Errorf("%w: dict keys not sorted: %q after %q", ErrInvalidFormat, keyStr, prevKey)
		}
		prevKey = keyStr
		first = false

		// Parse the value
		val, rest2, err := decodeValue(rest)
		if err != nil {
			return nil, nil, err
		}

		dict[keyStr] = val
		data = rest2
	}
}
