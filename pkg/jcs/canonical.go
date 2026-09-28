package jcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrNotValidJSON is returned when the input is not valid JSON or violates JCS rules.
var ErrNotValidJSON = errors.New("invalid JSON")

const invalidPattern uint64 = 0x7ff0000000000000

// Canonicalize takes any JSON-encoded bytes and returns the JCS-canonical
// representation according to RFC 8785. The output is deterministic regardless
// of how the input was originally serialized.
//
// Canonicalization steps:
//   - JSON is parsed strictly using encoding/json Decoder with UseNumber,
//     rejecting trailing characters or malformed tokens.
//   - Object members are sorted by key in UTF-16 code unit lexicographic order.
//   - Numbers are represented in ECMAScript canonical form (RFC 8785 §3.2.2.3),
//     including converting -0 to 0 and rejecting NaN/Infinity.
//   - Strings are escaped per RFC 8785 §3.2.2.2 (lone surrogates rejected).
//   - Only syntactically required whitespace is emitted.
func Canonicalize(input []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, ErrNotValidJSON
	}

	// Reject trailing garbage or extra tokens after the top-level value.
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrNotValidJSON
	}

	var buf bytes.Buffer
	if err := serializeCanonical(v, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Hash returns the SHA-256 digest of the JCS-canonical form of input.
func Hash(input []byte) ([]byte, error) {
	canon, err := Canonicalize(input)
	if err != nil {
		return nil, err
	}
	return sha256Hash(canon), nil
}

// HashHex returns the hex-encoded SHA-256 digest of the JCS-canonical form.
func HashHex(input []byte) (string, error) {
	h, err := Hash(input)
	if err != nil {
		return "", err
	}
	return hexEncode(h), nil
}

// MustCanonicalize is like Canonicalize but panics on error.
func MustCanonicalize(input []byte) []byte {
	out, err := Canonicalize(input)
	if err != nil {
		panic(err)
	}
	return out
}

// Marshal is a drop-in json.Marshal that returns JCS-canonical bytes.
func Marshal(v interface{}) ([]byte, error) {
	std, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(std)
}

// MarshalIndent is like Marshal. Note: JCS produces compact output without indentation.
func MarshalIndent(v interface{}, prefix, indent string) ([]byte, error) {
	std, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(std)
}

// ─────────────────────────────────────────────────────────────────
// Canonical Serializer
// ─────────────────────────────────────────────────────────────────

func serializeCanonical(v any, buf *bytes.Buffer) error {
	if v == nil {
		buf.WriteString("null")
		return nil
	}

	switch val := v.(type) {
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil

	case json.Number:
		f, err := val.Float64()
		if err != nil {
			return ErrNotValidJSON
		}
		s, err := numberToJSON(f)
		if err != nil {
			return ErrNotValidJSON
		}
		buf.WriteString(s)
		return nil

	case string:
		return serializeString(val, buf)

	case []any:
		buf.WriteByte('[')
		for i, elem := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := serializeCanonical(elem, buf); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil

	case map[string]any:
		buf.WriteByte('{')
		type member struct {
			key   string
			u16   []uint16
			value any
		}
		members := make([]member, 0, len(val))
		for k, v := range val {
			if !utf8.ValidString(k) {
				return ErrNotValidJSON
			}
			for _, r := range k {
				if r >= 0xD800 && r <= 0xDFFF {
					return ErrNotValidJSON
				}
			}
			members = append(members, member{
				key:   k,
				u16:   utf16.Encode([]rune(k)),
				value: v,
			})
		}
		sort.Slice(members, func(i, j int) bool {
			return utf16Less(members[i].u16, members[j].u16)
		})
		for i, m := range members {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := serializeString(m.key, buf); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := serializeCanonical(m.value, buf); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil

	default:
		return fmt.Errorf("unexpected json value type: %T", v)
	}
}

// serializeString escapes and quotes a JSON string per RFC 8785 §3.2.2.2.
func serializeString(s string, buf *bytes.Buffer) error {
	if !utf8.ValidString(s) {
		return ErrNotValidJSON
	}
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		b := s[i]
		switch b {
		case '"':
			buf.WriteString(`\"`)
			i++
		case '\\':
			buf.WriteString(`\\`)
			i++
		case '\b':
			buf.WriteString(`\b`)
			i++
		case '\f':
			buf.WriteString(`\f`)
			i++
		case '\n':
			buf.WriteString(`\n`)
			i++
		case '\r':
			buf.WriteString(`\r`)
			i++
		case '\t':
			buf.WriteString(`\t`)
			i++
		default:
			if b < 0x20 {
				// Lowercase \u00xx escape for ASCII control chars.
				buf.WriteString(fmt.Sprintf("\\u%04x", b))
				i++
			} else {
				r, size := utf8.DecodeRuneInString(s[i:])
				if r == utf8.RuneError && size == 1 {
					return ErrNotValidJSON
				}
				// RFC 8785: Lone surrogates (U+D800 - U+DFFF) are prohibited.
				if r >= 0xD800 && r <= 0xDFFF {
					return ErrNotValidJSON
				}
				buf.WriteString(s[i : i+size])
				i += size
			}
		}
	}
	buf.WriteByte('"')
	return nil
}

// numberToJSON formats an IEEE-754 double precision float per ECMAScript / RFC 8785 §3.2.2.3.
func numberToJSON(ieeeF64 float64) (string, error) {
	ieeeU64 := math.Float64bits(ieeeF64)

	// Special case: NaN and Infinity are invalid in JSON.
	if (ieeeU64 & invalidPattern) == invalidPattern {
		return "", errors.New("invalid JSON number: " + strconv.FormatUint(ieeeU64, 16))
	}

	// Special case: eliminate "-0" as mandated by RFC 8785 §3.2.2.3.
	if ieeeF64 == 0 {
		return "0", nil
	}

	var sign string = ""
	if ieeeF64 < 0 {
		ieeeF64 = -ieeeF64
		sign = "-"
	}

	// ECMAScript unique format rules:
	// Format as fixed if >= 1e-6 and < 1e+21, otherwise exponential.
	var format byte = 'e'
	if ieeeF64 < 1e+21 && ieeeF64 >= 1e-6 {
		format = 'f'
	}

	es6Formatted := strconv.FormatFloat(ieeeF64, format, -1, 64)

	// Minor cleanup for exponential format: Go outputs "1e+09", ECMAScript requires "1e+9".
	exponent := strings.IndexByte(es6Formatted, 'e')
	if exponent > 0 && len(es6Formatted) > exponent+2 && es6Formatted[exponent+2] == '0' {
		es6Formatted = es6Formatted[:exponent+2] + es6Formatted[exponent+3:]
	}

	return sign + es6Formatted, nil
}

// utf16Less compares two UTF-16 slices lexicographically (RFC 8785 §3.2.3).
func utf16Less(a, b []uint16) bool {
	min := len(a)
	if len(b) < min {
		min = len(b)
	}
	for i := 0; i < min; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
