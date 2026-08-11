// RFC 8785 JSON Canonicalization Scheme (JCS) serialization.
//
// All BFF/participant adapters compute the same command fingerprint
// (consistency-and-compensation.md), so the canonical bytes must be
// independent of the Go side that produced them. Rules implemented here:
//
//   - object keys sorted by UTF-16 code units (not byte order, not runes);
//   - strings: only '"', '\' and U+0000..U+001F escaped; U+0008/9/A/C/D use
//     \b \t \n \f \r, the rest use \u00xx lowercase; every other character
//     (including U+2028/2029 and C1 controls) MUST NOT be escaped;
//   - numbers: no leading zeros, no '+', no trailing '.0', shortest form;
//     exponent only when decimal notation cannot represent the value;
//   - no whitespace anywhere.
//
// The value set is deliberately narrow (the fingerprint args): nil, bool,
// string, integer types, float64, []any, []int64, []string, map[string]any.
// Anything else is an error so an accidentally added value type cannot
// silently produce divergent bytes across adapters.
package consistency

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const hexLower = "0123456789abcdef"

// MarshalJCS serializes v per RFC 8785. Map keys must be strings; key order
// is the UTF-16 code unit order, so the input map order never matters.
func MarshalJCS(v any) ([]byte, error) {
	var buf []byte
	var err error
	if buf, err = appendJCS(nil, v); err != nil {
		return nil, err
	}
	return buf, nil
}

func appendJCS(buf []byte, v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return append(buf, "null"...), nil
	case bool:
		if t {
			return append(buf, "true"...), nil
		}
		return append(buf, "false"...), nil
	case string:
		return appendJCSString(buf, t)
	case int:
		return strconv.AppendInt(buf, int64(t), 10), nil
	case int64:
		return strconv.AppendInt(buf, t, 10), nil
	case uint64:
		return strconv.AppendUint(buf, t, 10), nil
	case float64:
		return appendJCSNumber(buf, t)
	case []any:
		buf = append(buf, '[')
		for i, item := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			var err error
			if buf, err = appendJCS(buf, item); err != nil {
				return nil, err
			}
		}
		return append(buf, ']'), nil
	case []int64:
		buf = append(buf, '[')
		for i, item := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = strconv.AppendInt(buf, item, 10)
		}
		return append(buf, ']'), nil
	case []string:
		buf = append(buf, '[')
		for i, item := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			var err error
			if buf, err = appendJCSString(buf, item); err != nil {
				return nil, err
			}
		}
		return append(buf, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortUTF16(keys)
		buf = append(buf, '{')
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			var err error
			if buf, err = appendJCSString(buf, k); err != nil {
				return nil, err
			}
			buf = append(buf, ':')
			if buf, err = appendJCS(buf, t[k]); err != nil {
				return nil, err
			}
		}
		return append(buf, '}'), nil
	default:
		return nil, fmt.Errorf("jcs: unsupported value type %T", v)
	}
}

// appendJCSString escapes exactly per RFC 8785 §3.2.2.2 and rejects invalid
// UTF-8 (validated input contract; a divergent byte sequence must never
// silently pass through).
func appendJCSString(buf []byte, s string) ([]byte, error) {
	buf = append(buf, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x20 {
			switch c {
			case '\b':
				buf = append(buf, '\\', 'b')
			case '\t':
				buf = append(buf, '\\', 't')
			case '\n':
				buf = append(buf, '\\', 'n')
			case '\f':
				buf = append(buf, '\\', 'f')
			case '\r':
				buf = append(buf, '\\', 'r')
			default:
				buf = append(buf, '\\', 'u', '0', '0', hexLower[c>>4], hexLower[c&0x0f])
			}
			i++
			continue
		}
		if c == '"' || c == '\\' {
			buf = append(buf, '\\', c)
			i++
			continue
		}
		if c < utf8.RuneSelf {
			buf = append(buf, c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return nil, errors.New("jcs: invalid UTF-8 in string")
		}
		buf = append(buf, s[i:i+size]...)
		i += size
	}
	return append(buf, '"'), nil
}

// appendJCSNumber serializes per RFC 8785 §3.2.2.1: finite values only, no
// leading zeros or '+', no trailing fraction for integral values, decimal
// notation whenever the shortest representation can express the value,
// otherwise lowercase 'e' exponent without '+'.
func appendJCSNumber(buf []byte, f float64) ([]byte, error) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, errors.New("jcs: non-finite number")
	}
	if f == 0 {
		// IEEE-754 negative zero is a distinct value; RFC 8785 forbids "-0".
		return append(buf, '0'), nil
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		// Integral and expressible with at most 21 digits: decimal form.
		return strconv.AppendFloat(buf, f, 'f', -1, 64), nil
	}
	if math.Abs(f) >= 1e-6 && math.Abs(f) < 1e21 {
		// Decimal form with fraction ('f' -1 never emits trailing zeros).
		return strconv.AppendFloat(buf, f, 'f', -1, 64), nil
	}
	// Exponent notation required; Go emits a '+' and zero-pads the exponent,
	// both of which RFC 8785 forbids ("1e30" and "2e-3", never "1e+30" or
	// "2e-03").
	s := strconv.FormatFloat(f, 'e', -1, 64)
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		exp := s[i+1:]
		sign := ""
		if len(exp) > 0 && (exp[0] == '+' || exp[0] == '-') {
			if exp[0] == '-' {
				sign = "-"
			}
			exp = strings.TrimLeft(exp[1:], "0")
		} else {
			exp = strings.TrimLeft(exp, "0")
		}
		if exp == "" {
			exp = "0"
		}
		s = s[:i] + "e" + sign + exp
	}
	return append(buf, s...), nil
}

// sortUTF16 sorts keys by UTF-16 code unit order (RFC 8785 §3.2.2.3): case-
// sensitive ('Z' < 'a'), astral characters sort as their surrogate pairs.
func sortUTF16(keys []string) {
	sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
}

// utf16Less compares two strings by their UTF-16 code unit sequences; the
// shorter sequence is smaller when both are equal prefixes.
func utf16Less(a, b string) bool {
	ca := utf16CodeUnits(a)
	cb := utf16CodeUnits(b)
	for i := 0; i < len(ca) && i < len(cb); i++ {
		if ca[i] != cb[i] {
			return ca[i] < cb[i]
		}
	}
	return len(ca) < len(cb)
}

// utf16CodeUnits decodes valid UTF-8 into UTF-16 code units; non-BMP runes
// become their high/low surrogate pair.
func utf16CodeUnits(s string) []uint16 {
	var units []uint16
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r <= 0xFFFF {
			units = append(units, uint16(r))
		} else {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		}
		i += size
	}
	return units
}
