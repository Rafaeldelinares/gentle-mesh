package jcs

import (
	"bytes"
	"encoding/json"
	"testing"
)

// ─────────────────────────────────────────────────────────────────
// Test cases from RFC 8785 appendix A
// ─────────────────────────────────────────────────────────────────

func TestRFC8785_Example1(t *testing.T) {
	// RFC 8785 Appendix A.1 — Numbers
	input := `1e2`
	canon, err := Canonicalize([]byte(input))
	if err != nil {
		t.Fatalf("Canonicalize(%q) error: %v", input, err)
	}
	// 1e2 = 100 in ECMAScript canonical form.
	got := string(canon)
	if got != "100" {
		t.Errorf("Canonicalize(%q) = %q, want %q", input, got, "100")
	}
}

func TestRFC8785_Example2(t *testing.T) {
	// RFC 8785 Appendix A.2 — Numbers
	input := `1.0`
	canon, err := Canonicalize([]byte(input))
	if err != nil {
		t.Fatalf("Canonicalize(%q) error: %v", input, err)
	}
	// 1.0 = 1 in ECMAScript canonical form.
	got := string(canon)
	if got != "1" {
		t.Errorf("Canonicalize(%q) = %q, want %q", input, got, "1")
	}
}

func TestRFC8785_Example3(t *testing.T) {
	// RFC 8785 Appendix A.3 — Object key ordering
	inputs := []string{
		`{"a":1,"b":2}`,
		`{"b":2,"a":1}`,
		`  {"a":1,"b":2}  `, // whitespace only
	}
	var canonicalsList []string
	for _, input := range inputs {
		canon, err := Canonicalize([]byte(input))
		if err != nil {
			t.Fatalf("Canonicalize(%q) error: %v", input, err)
		}
		canonicalsList = append(canonicalsList, string(canon))
	}
	// All must canonicalize to the same output.
	for i, got := range canonicalsList {
		if got != `{"a":1,"b":2}` {
			t.Errorf("Canonicalize(%q) = %q, want %q", inputs[i], got, `{"a":1,"b":2}`)
		}
	}
}

func TestRFC8785_Example4(t *testing.T) {
	// RFC 8785 — Unicode key ordering (B < a in ASCII, but B > a in UTF-16?).
	// "B" (0x0042) < "a" (0x0061) in both ASCII and UTF-16.
	input := `{"a":1,"B":2}`
	canon, err := Canonicalize([]byte(input))
	if err != nil {
		t.Fatalf("Canonicalize(%q) error: %v", input, err)
	}
	// B < a lexicographically by code unit (0x42 < 0x61).
	got := string(canon)
	want := `{"B":2,"a":1}`
	if got != want {
		t.Errorf("Canonicalize(%q) = %q, want %q", input, got, want)
	}
}

// ─────────────────────────────────────────────────────────────────
// Core canonicalization tests
// ─────────────────────────────────────────────────────────────────

func TestCanonicalize_ObjectKeysSorted(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "z before a",
			input: `{"z":1,"a":2}`,
			want:  `{"a":2,"z":1}`,
		},
		{
			name:  "z before aa",
			input: `{"z":1,"aa":2}`,
			want:  `{"aa":2,"z":1}`,
		},
		{
			name:  "empty object",
			input: `{}`,
			want:  `{}`,
		},
		{
			name:  "single key",
			input: `{"key":"value"}`,
			want:  `{"key":"value"}`,
		},
		{
			name:  "nested object",
			input: `{"b":{"a":1,"z":2},"a":3}`,
			want:  `{"a":3,"b":{"a":1,"z":2}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tt.input))
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCanonicalize_Numbers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "integer", input: `30`, want: `30`},

		{name: "float 1.0", input: `1.0`, want: `1`},
		{name: "float 1.00", input: `1.00`, want: `1`},
		{name: "float 0.5", input: `0.5`, want: `0.5`},
		{name: "scientific 1e2", input: `1e2`, want: `100`},
		{name: "scientific 1E2", input: `1E2`, want: `100`},
		{name: "scientific 1e+2", input: `1e+2`, want: `100`},
		{name: "scientific 1e-2", input: `1e-2`, want: `0.01`},
		{name: "scientific 1.5e2", input: `1.5e2`, want: `150`},
		{name: "negative -1.0", input: `-1.0`, want: `-1`},
		{name: "negative -0", input: `-0`, want: `0`},
		{name: "zero", input: `0`, want: `0`},
		{name: "float 0.0", input: `0.0`, want: `0`},
		{name: "scientific 0e10", input: `0e10`, want: `0`},
		{name: "scientific 123456789e-9", input: `123456789e-9`, want: `0.123456789`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tt.input))
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCanonicalize_Strings(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple string",
			input: `"hello"`,
			want:  `"hello"`,
		},
		{
			name:  "escaped newline",
			input: `"hello\nworld"`,
			want:  `"hello\nworld"`,
		},
		{
			name:  "escaped tab",
			input: `"hello\tworld"`,
			want:  `"hello\tworld"`,
		},
		{
			name:  "escaped backslash",
			input: `"hello\\world"`,
			want:  `"hello\\world"`,
		},
		{
			name:  "escaped quote",
			input: `"hello\"world"`,
			want:  `"hello\"world"`,
		},
		{
			name:  "unicode chars preserved",
			input: `"café"`,
			want:  `"café"`,
		},
		{
			name:  "emoji preserved",
			input: `"👋🚀"`,
			want:  `"👋🚀"`,
		},
		{
			name:  "empty string",
			input: `""`,
			want:  `""`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tt.input))
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCanonicalize_Arrays(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple array",
			input: `[1,2,3]`,
			want:  `[1,2,3]`,
		},
		{
			name:  "mixed array",
			input: `[true,"hello",null,42]`,
			want:  `[true,"hello",null,42]`,
		},
		{
			name:  "array with object",
			input: `[{"z":1,"a":2}]`,
			want:  `[{"a":2,"z":1}]`,
		},
		{
			name:  "empty array",
			input: `[]`,
			want:  `[]`,
		},
		{
			name:  "nested arrays",
			input: `[[3,2,1],[1,2,3]]`,
			want:  `[[3,2,1],[1,2,3]]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tt.input))
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCanonicalize_BooleansAndNull(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`true`, `true`},
		{`false`, `false`},
		{`null`, `null`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := Canonicalize([]byte(tt.input))
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCanonicalize_Complex(t *testing.T) {
	input := `{
  "z": true,
  "a": 1,
  "arr": [3, 1, 2],
  "obj": {"y": "b", "x": "a"}
}`
	want := `{"a":1,"arr":[3,1,2],"obj":{"x":"a","y":"b"},"z":true}`

	got, err := Canonicalize([]byte(input))
	if err != nil {
		t.Fatalf("Canonicalize error: %v", err)
	}
	if string(got) != want {
		t.Errorf("Complex canonicalize = %q, want %q", got, want)
	}
}

// ─────────────────────────────────────────────────────────────────
// Determinism tests
// ─────────────────────────────────────────────────────────────────

func TestCanonicalize_Deterministic(t *testing.T) {
	// A value must canonicalize to the same output every time.
	inputs := []string{
		`{"z":1,"b":2,"a":3,"y":4,"x":5}`,
		`{"a":3,"b":2,"x":5,"y":4,"z":1}`,
		`  {  "z" : 1 , "b" : 2 , "a" : 3 , "y" : 4 , "x" : 5 }  `,
		`{"b":2,"x":5,"a":3,"z":1,"y":4}`,
	}

	var first []byte
	for i, input := range inputs {
		canon, err := Canonicalize([]byte(input))
		if err != nil {
			t.Fatalf("Canonicalize(%q) error: %v", input, err)
		}
		if i == 0 {
			first = canon
		} else if !bytes.Equal(canon, first) {
			t.Errorf("Canonicalize(%q) = %q differs from first output %q", input, canon, first)
		}
	}
}

func TestCanonicalize_Idempotent(t *testing.T) {
	// Canonicalize(canonicalize(x)) == canonicalize(x).
	inputs := []string{
		`{"a":1,"b":[true,false,null,{"z":1,"a":2}]}`,
		`[1,2,3,"hello","world"]`,
		`{"nested":{"arr":[1,2,3],"obj":{"a":1}}}`,
	}

	for _, input := range inputs {
		canon1, err := Canonicalize([]byte(input))
		if err != nil {
			t.Fatalf("First Canonicalize(%q) error: %v", input, err)
		}
		canon2, err := Canonicalize(canon1)
		if err != nil {
			t.Fatalf("Second Canonicalize error: %v", err)
		}
		if !bytes.Equal(canon1, canon2) {
			t.Errorf("Canonicalize not idempotent for %q: %q != %q", input, canon1, canon2)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// Hash tests
// ─────────────────────────────────────────────────────────────────

func TestHash_Deterministic(t *testing.T) {
	inputs := []string{
		`{"z":1,"a":2}`,
		`{"a":2,"z":1}`,
		`  { "z" : 1 , "a" : 2 }  `,
	}

	var first string
	for i, input := range inputs {
		h, err := HashHex([]byte(input))
		if err != nil {
			t.Fatalf("Hash(%q) error: %v", input, err)
		}
		if i == 0 {
			first = h
		} else if h != first {
			t.Errorf("Hash(%q) = %q differs from first hash %q", input, h, first)
		}
	}
}

func TestHashHex(t *testing.T) {
	// Hash of `1` is deterministic.
	h, err := HashHex([]byte(`1`))
	if err != nil {
		t.Fatalf("HashHex error: %v", err)
	}
	// SHA-256 of "1" (as JCS-canonical, which for "1" is just "1").
	// 32-byte hash = 64 hex chars.
	if len(h) != 64 {
		t.Errorf("HashHex length = %d, want 64", len(h))
	}

	// Verify it's consistent.
	h2, _ := HashHex([]byte(`1`))
	if h != h2 {
		t.Errorf("HashHex not deterministic: %q != %q", h, h2)
	}
}

// ─────────────────────────────────────────────────────────────────
// Marshal convenience
// ─────────────────────────────────────────────────────────────────

func TestMarshal(t *testing.T) {
	type Doc struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	doc := Doc{Name: "Beto", Value: 42}
	out, err := Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Must be valid JSON.
	var check map[string]interface{}
	if err := json.Unmarshal(out, &check); err != nil {
		t.Errorf("Marshal output %q is not valid JSON: %v", out, err)
	}

	// Must be deterministic.
	out2, _ := Marshal(doc)
	if !bytes.Equal(out, out2) {
		t.Errorf("Marshal not deterministic: %q != %q", out, out2)
	}
}

func TestMarshal_UTF8Preserved(t *testing.T) {
	doc := map[string]string{
		"greeting": "¡Hola, Café! 你好 👋",
	}
	out, err := Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Verify round-trip.
	var check map[string]string
	if err := json.Unmarshal(out, &check); err != nil {
		t.Errorf("Marshal output %q is not valid JSON: %v", out, err)
	}
	if check["greeting"] != doc["greeting"] {
		t.Errorf("Round-trip failed: got %q, want %q", check["greeting"], doc["greeting"])
	}
}

// ─────────────────────────────────────────────────────────────────
// Error cases
// ─────────────────────────────────────────────────────────────────

func TestCanonicalize_InvalidJSON(t *testing.T) {
	invalid := []string{
		``,
		`   `,
		`{`,
		`}`,
		`[`,
		`not json at all`,
		`"unclosed string`,
		`{invalid}`,
	}

	for _, input := range invalid {
		_, err := Canonicalize([]byte(input))
		if err == nil {
			t.Errorf("Canonicalize(%q) expected error, got nil", input)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// Benchmark
// ─────────────────────────────────────────────────────────────────

func BenchmarkCanonicalize_Small(b *testing.B) {
	input := []byte(`{"z":1,"a":2,"m":[3,1,4,1,5,9],"obj":{"x":true,"y":false}}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Canonicalize(input)
	}
}

func BenchmarkCanonicalize_Large(b *testing.B) {
	// Simulate a large envelope with many fields.
	var buf bytes.Buffer
	buf.WriteString(`{"contract_id":"uuid-123","emitter":"agent-a","executor":"agent-b","territory":{"repo":"github.com/org/repo","branch":"feature/test"},"preconditions":[`)
	for i := 0; i < 50; i++ {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString(`{"type":"command_exit_code","command":"go build ./...","expected":0}`)
	}
	buf.WriteString(`],"assertions":[`)
	for i := 0; i < 50; i++ {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString(`{"id":"test_` + string(rune('a'+i%26)) + `","type":"command_exit_code","command":"go test ./pkg/` + string(rune('a'+i%26)) + `/...","expected":0}`)
	}
	buf.WriteString(`],"timeout_seconds":300}`)
	input := buf.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Canonicalize(input)
	}
}

func BenchmarkHash_Small(b *testing.B) {
	input := []byte(`{"a":1,"z":2}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Hash(input)
	}
}
