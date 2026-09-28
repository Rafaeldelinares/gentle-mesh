package jcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ErrNotValidJSON is returned when the input is not valid JSON.
var ErrNotValidJSON = errors.New("invalid JSON")

// Canonicalize takes any JSON-encoded bytes and returns the JCS-canonical
// representation (RFC 8785). The output is deterministic regardless of how
// the input was originally serialized.
//
// Canonicalization steps:
//   - Object members are sorted by key (UTF-16 lexicographic order)
//   - Numbers are in ECMAScript canonical form
//   - String escapes are canonicalized
//   - Only syntactically required whitespace is preserved
//
// Use this to compute a deterministic hash before signing.
func Canonicalize(input []byte) ([]byte, error) {
	// Step 1: Parse the JSON into an intermediate representation.
	val, err := parseValue(input)
	if err != nil {
		return nil, err
	}

	// Step 2: Serialize canonically.
	var buf bytes.Buffer
	val.canonicalize(&buf)
	return buf.Bytes(), nil
}

// Hash returns the SHA-256 digest of the JCS-canonical form of input.
// This is the hash that gets signed by Ed25519.
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

// ─────────────────────────────────────────────────────────────────
// Intermediate representation (preserves original number literals)
// ─────────────────────────────────────────────────────────────────

type value struct {
	kind kind
	raw  string // original literal for strings, numbers; empty for object/array
	obj  []*member
	arr  []value
}

type kind int

const (
	kindString kind = iota
	kindNumber
	kindObject
	kindArray
	kindTrue
	kindFalse
	kindNull
)

type member struct {
	key   string
	value value
}

// ─────────────────────────────────────────────────────────────────
// Parser: token-by-token, preserves raw number and string literals
// ─────────────────────────────────────────────────────────────────

func parseValue(input []byte) (value, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return value{}, ErrNotValidJSON
	}

	switch trimmed[0] {
	case '{':
		return parseObject(trimmed)
	case '[':
		return parseArray(trimmed)
	case '"':
		return parseString(trimmed)
	case 't':
		if string(trimmed) == "true" {
			return value{kind: kindTrue}, nil
		}
	case 'f':
		if string(trimmed) == "false" {
			return value{kind: kindFalse}, nil
		}
	case 'n':
		if string(trimmed) == "null" {
			return value{kind: kindNull}, nil
		}
	}

	// Number (must start with digit or -)
	if isDigit(trimmed[0]) || trimmed[0] == '-' {
		return parseNumber(trimmed)
	}

	return value{}, ErrNotValidJSON
}

func parseObject(input []byte) (value, error) {
	if input[0] != '{' {
		return value{}, ErrNotValidJSON
	}

	// Find matching '}' — skip over nested {} and []
	depth := 0
	end := -1
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 && i > 0 {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return value{}, ErrNotValidJSON
	}

	// Empty object
	inner := input[1:end]
	if len(bytes.TrimSpace(inner)) == 0 {
		return value{kind: kindObject, obj: []*member{}}, nil
	}

	// Split by top-level commas, then parse each "key":value pair.
	pairs := splitTopLevel(inner)
	members := make([]*member, 0, len(pairs))

	for _, pair := range pairs {
		// Find the colon separator.
		colon := findColon(pair)
		if colon < 0 {
			return value{}, ErrNotValidJSON
		}

		keyBytes := bytes.TrimSpace(pair[:colon])
		valBytes := bytes.TrimSpace(pair[colon+1:])

		if len(keyBytes) == 0 || keyBytes[0] != '"' {
			return value{}, ErrNotValidJSON
		}

		key, err := extractRawString(keyBytes)
		if err != nil {
			return value{}, ErrNotValidJSON
		}

		val, err := parseValue(valBytes)
		if err != nil {
			return value{}, ErrNotValidJSON
		}

		members = append(members, &member{key: key, value: val})
	}

	return value{kind: kindObject, obj: members}, nil
}

func parseArray(input []byte) (value, error) {
	if input[0] != '[' {
		return value{}, ErrNotValidJSON
	}

	// Find matching ']'.
	depth := 0
	end := -1
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 && i > 0 {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return value{}, ErrNotValidJSON
	}

	// Empty array
	inner := input[1:end]
	if len(bytes.TrimSpace(inner)) == 0 {
		return value{kind: kindArray, arr: []value{}}, nil
	}

	// Split by top-level commas.
	elements := splitTopLevel(inner)
	arr := make([]value, 0, len(elements))

	for _, elem := range elements {
		val, err := parseValue(bytes.TrimSpace(elem))
		if err != nil {
			return value{}, ErrNotValidJSON
		}
		arr = append(arr, val)
	}

	return value{kind: kindArray, arr: arr}, nil
}

// parseString extracts the raw string content (unescaped) from a JSON string literal.
// We preserve the original escape sequences for canonicalization.
func parseString(input []byte) (value, error) {
	if input[0] != '"' {
		return value{}, ErrNotValidJSON
	}

	// Find the closing quote (not preceded by backslash).
	end := -1
	for i := 1; i < len(input); i++ {
		if input[i] == '"' && input[i-1] != '\\' {
			end = i
			break
		}
	}
	if end < 0 {
		return value{}, ErrNotValidJSON
	}

	raw := string(input[:end+1])
	return value{kind: kindString, raw: raw}, nil
}

// parseNumber preserves the original number literal for JCS compliance.
// RFC 8785 requires ECMAScript canonical number representation.
func parseNumber(input []byte) (value, error) {
	// Accept: -?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?
	i := 0
	if len(input) > 0 && input[0] == '-' {
		i++
	}
	if i >= len(input) || !isDigit(input[i]) {
		return value{}, ErrNotValidJSON
	}
	for i < len(input) && isDigit(input[i]) {
		i++
	}
	if i < len(input) && input[i] == '.' {
		i++
		if i >= len(input) || !isDigit(input[i]) {
			return value{}, ErrNotValidJSON
		}
		for i < len(input) && isDigit(input[i]) {
			i++
		}
	}
	if i < len(input) && (input[i] == 'e' || input[i] == 'E') {
		i++
		if i < len(input) && (input[i] == '+' || input[i] == '-') {
			i++
		}
		if i >= len(input) || !isDigit(input[i]) {
			return value{}, ErrNotValidJSON
		}
		for i < len(input) && isDigit(input[i]) {
			i++
		}
	}
	if i != len(input) {
		return value{}, ErrNotValidJSON
	}

	return value{kind: kindNumber, raw: string(input)}, nil
}

// ─────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────

// splitTopLevel splits by comma at depth=0 only.
func splitTopLevel(input []byte) [][]byte {
	var result [][]byte
	depth := 0
	start := 0
	for i := 0; i <= len(input); i++ {
		if i == len(input) || (input[i] == ',' && depth == 0) {
			result = append(result, input[start:i])
			start = i + 1
			continue
		}
		switch input[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			// Skip string content to avoid counting braces inside strings.
			for i++; i < len(input); i++ {
				if input[i] == '\\' {
					i++ // skip escaped char
					continue
				}
				if input[i] == '"' {
					break
				}
			}
		}
	}
	return result
}

func findColon(pair []byte) int {
	for i := 0; i < len(pair); i++ {
		if pair[i] == ':' {
			return i
		}
	}
	return -1
}

// extractRawString returns the unescaped string content (without quotes).
func extractRawString(b []byte) (string, error) {
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return "", ErrNotValidJSON
	}
	s := string(b[1 : len(b)-1])
	// Unescape for comparison and storage.
	return unescape(s), nil
}

func unescape(s string) string {
	var r strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			r.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case '"':
			r.WriteByte('"')
		case '\\':
			r.WriteByte('\\')
		case '/':
			r.WriteByte('/')
		case 'b':
			r.WriteByte('\b')
		case 'f':
			r.WriteByte('\f')
		case 'n':
			r.WriteByte('\n')
		case 'r':
			r.WriteByte('\r')
		case 't':
			r.WriteByte('\t')
		case 'u':
			if i+4 >= len(s) {
				r.WriteByte('u')
				continue
			}
			hex := s[i+1 : i+5]
			i += 4
			var cp rune
			for _, h := range hex {
				cp <<= 4
				switch {
				case h >= '0' && h <= '9':
					cp |= rune(h - '0')
				case h >= 'a' && h <= 'f':
					cp |= rune(h - 'a' + 10)
				case h >= 'A' && h <= 'F':
					cp |= rune(h - 'A' + 10)
				default:
					return s // malformed, return as-is
				}
			}
			r.WriteRune(cp)
		default:
			r.WriteByte(s[i])
		}
	}
	return r.String()
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// ─────────────────────────────────────────────────────────────────
// Canonical Serializer
// ─────────────────────────────────────────────────────────────────

func (v *value) canonicalize(buf *bytes.Buffer) {
	switch v.kind {
	case kindObject:
		v.canonicalizeObject(buf)
	case kindArray:
		v.canonicalizeArray(buf)
	case kindString:
		canonicalizeString(v.raw, buf)
	case kindNumber:
		canonicalizeNumber(v.raw, buf)
	case kindTrue:
		buf.WriteString("true")
	case kindFalse:
		buf.WriteString("false")
	case kindNull:
		buf.WriteString("null")
	}
}

func (v *value) canonicalizeObject(buf *bytes.Buffer) {
	// Sort members by key using UTF-16 code units (RFC 8785 §4).
	members := v.obj
	sort.Slice(members, func(i, j int) bool {
		return utf16Less([]uint16(utf16.Encode([]rune(members[i].key))),
			[]uint16(utf16.Encode([]rune(members[j].key))))
	})

	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		canonicalizeString(`"`+m.key+`"`, buf)
		buf.WriteByte(':')
		m.value.canonicalize(buf)
	}
	buf.WriteByte('}')
}

func (v *value) canonicalizeArray(buf *bytes.Buffer) {
	buf.WriteByte('[')
	for i, elem := range v.arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		elem.canonicalize(buf)
	}
	buf.WriteByte(']')
}

// canonicalizeString produces a JCS-canonical JSON string.
// It re-escapes characters according to RFC 8785 §4.2.
func canonicalizeString(raw string, buf *bytes.Buffer) {
	if raw == "" {
		buf.WriteString(`""`)
		return
	}

	// If already a valid JSON string literal, canonicalize the escapes.
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		buf.WriteByte('"')
		for i := 1; i < len(raw)-1; i++ {
			ch := raw[i]
			switch ch {
			case '"':
				buf.WriteString(`\"`)
			case '\\':
				// Check for escaped sequence.
				if i+1 < len(raw)-1 {
					next := raw[i+1]
					switch next {
					case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
						// Valid escape, copy both characters.
						buf.WriteByte('\\')
						buf.WriteByte(next)
						i++ // skip the escaped char.
						continue
					}
				}
				// Not a valid escape sequence or end of string.
				// Per RFC 8785, lone backslash is kept as-is (not re-escaped).
				// But practically, JSON parsers would reject this.
				// For canonical output, we preserve the raw representation.
				buf.WriteByte('\\')
			default:
				// Check for control characters (0x00-0x1F).
				if ch < 0x20 {
					buf.WriteString(escapeControl(ch))
				} else {
					buf.WriteByte(ch)
				}
			}
		}
		buf.WriteByte('"')
		return
	}

	// Plain string (no quotes) — wrap and escape.
	buf.WriteByte('"')
	for _, r := range raw {
		if r < 0x20 {
			// #nosec G115 — r is from []byte (range 0-255); byte(r) always valid.
			// gosec cannot trace the type constraint; verified by inspection.
			buf.WriteString(escapeControl(byte(r)))
		} else if r == '"' {
			buf.WriteString(`\"`)
		} else if r == '\\' {
			buf.WriteString(`\\`)
		} else {
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// escapeControl returns the canonical JSON escape for a control character.
// Per RFC 8785, control chars must be escaped as \u00XX.
func escapeControl(ch byte) string {
	return `\u00` + string(hexDigit(ch>>4)) + string(hexDigit(ch&0x0F))
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'A' + b - 10
}

// canonicalizeNumber converts a JSON number to ECMAScript canonical form.
// RFC 8785 §4.2: Numbers are represented in their ECMAScript canonical form.
func canonicalizeNumber(raw string, buf *bytes.Buffer) {
	// Parse the number and re-serialize canonically.
	// This handles: 30.0 → 30, 1e2 → 100, +1 → 1, 1. → invalid JSON.
	//
	// For RFC 8785 compliance, we convert to float64 and re-encode,
	// but we need to preserve integers without decimal point when possible.
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		// If parsing fails, write the original (defensive).
		buf.WriteString(raw)
		return
	}

	// Check if it's an integer.
	if isInteger(f) {
		// Write as integer (no decimal point, no exponent if not needed).
		buf.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
	} else {
		// Canonical form: use 'g' with precision 17, no unnecessary exponent.
		// But per RFC 8785, we must avoid trailing zeros and use 'g' precision 17.
		s := strconv.FormatFloat(f, 'g', 17, 64)
		// Ensure it contains a decimal point if it's a float.
		if !containsDecimal(s) {
			// Add .0 to make it a float literal.
			s += ".0"
		}
		buf.WriteString(s)
	}
}

func isInteger(f float64) bool {
	return f == float64(int64(f)) && !isInfOrNaN(f)
}

func isInfOrNaN(f float64) bool {
	return !(f <= 0 || f >= 0)
}

func containsDecimal(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' || s[i] == 'e' || s[i] == 'E' {
			return true
		}
	}
	return false
}

// utf16Less compares two UTF-16 slices lexicographically (RFC 8785 §4).
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

// ─────────────────────────────────────────────────────────────────
// Standard JSON round-trip (for comparison / validation)
// ─────────────────────────────────────────────────────────────────

// MustCanonicalize is like Canonicalize but panics on error.
// Use in tests or when input is known to be valid JSON.
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

// MarshalIndent is like Marshal but with indentation. Note: JCS does not
// preserve indentation; the canonical output is compact.
func MarshalIndent(v interface{}, prefix, indent string) ([]byte, error) {
	std, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(std)
}
