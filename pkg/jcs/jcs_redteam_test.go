package jcs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRedTeam_TrailingGarbageAndMalformedJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"extra closing brace", `{"a":1}}`},
		{"extra closing bracket", `[1,2]]`},
		{"concatenated objects", `{"a":1} {"b":2}`},
		{"trailing garbage after object", `{"a":1} garbage`},
		{"trailing garbage after array", `[1,2] extra`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tc.input))
			if err == nil {
				t.Fatalf("expected error for malformed input %q, got nil (canonicalized to %q)", tc.input, string(got))
			}
		})
	}
}

func TestRedTeam_BracesAndBracketsInsideStrings(t *testing.T) {
	// A string containing braces/brackets must not cause truncation of fields that follow it.
	input := `{"msg":"cierre } falso","z":1}`
	canon, err := Canonicalize([]byte(input))
	if err != nil {
		t.Fatalf("Canonicalize failed: %v", err)
	}

	// Must contain both fields "msg" and "z"
	want := `{"msg":"cierre } falso","z":1}`
	if string(canon) != want {
		t.Fatalf("Canonicalize truncated or corrupted fields: got %q, want %q", string(canon), want)
	}

	// Array with brackets inside string
	inputArr := `["elem ] 1", "elem 2"]`
	canonArr, err := Canonicalize([]byte(inputArr))
	if err != nil {
		t.Fatalf("Canonicalize array failed: %v", err)
	}
	wantArr := `["elem ] 1","elem 2"]`
	if string(canonArr) != wantArr {
		t.Fatalf("Canonicalize array corrupted: got %q, want %q", string(canonArr), wantArr)
	}
}

func TestRedTeam_HashCollisionOnStringWithBraces(t *testing.T) {
	// Two tasks with the same intent containing "}", but with DIFFERENT repo, branch, and command.
	// In vulnerable code, parsing stops at "}" so only the first part is parsed (or fields after "}" are lost),
	// producing identical canonical forms and identical hashes!
	taskA := `{"intent":"Fix parser } urgently","repo":"repo-A","branch":"main","command":"make build"}`
	taskB := `{"intent":"Fix parser } urgently","repo":"repo-B","branch":"feature","command":"rm -rf /"}`

	canonA, errA := Canonicalize([]byte(taskA))
	if errA != nil {
		t.Fatalf("Canonicalize(taskA) error: %v", errA)
	}
	canonB, errB := Canonicalize([]byte(taskB))
	if errB != nil {
		t.Fatalf("Canonicalize(taskB) error: %v", errB)
	}

	hashA, errHA := Hash([]byte(taskA))
	if errHA != nil {
		t.Fatalf("Hash(taskA) error: %v", errHA)
	}
	hashB, errHB := Hash([]byte(taskB))
	if errHB != nil {
		t.Fatalf("Hash(taskB) error: %v", errHB)
	}

	if bytes.Equal(hashA, hashB) {
		t.Fatalf("SECURITY VULNERABILITY: Hash collision detected! Both distinct payloads produced identical hash %x (canonA=%q, canonB=%q)",
			hashA, string(canonA), string(canonB))
	}

	if string(canonA) == string(canonB) {
		t.Fatalf("SECURITY VULNERABILITY: Canonical collision detected! Both distinct payloads canonicalized to %q", string(canonA))
	}
}

func TestHashCollision_RealGoStructs(t *testing.T) {
	// Verification on real Go structs passed through jcs.Marshal and HashHexString.
	type TaskStruct struct {
		Intent  string `json:"intent"`
		Repo    string `json:"repo"`
		Branch  string `json:"branch"`
		Command string `json:"command"`
	}

	taskA := TaskStruct{
		Intent:  "Fix parser } urgently",
		Repo:    "github.com/org/repo-A",
		Branch:  "main",
		Command: "make build",
	}
	taskB := TaskStruct{
		Intent:  "Fix parser } urgently",
		Repo:    "github.com/org/repo-B",
		Branch:  "feature",
		Command: "rm -rf /",
	}

	bytesA, err := Marshal(taskA)
	if err != nil {
		t.Fatalf("Marshal(taskA) error: %v", err)
	}
	bytesB, err := Marshal(taskB)
	if err != nil {
		t.Fatalf("Marshal(taskB) error: %v", err)
	}

	if bytes.Equal(bytesA, bytesB) {
		t.Fatalf("SECURITY VULNERABILITY: distinct Go structs marshaled to identical bytes: %s", string(bytesA))
	}

	hashA, err := HashHexString(taskA)
	if err != nil {
		t.Fatalf("HashHexString(taskA) error: %v", err)
	}
	hashB, err := HashHexString(taskB)
	if err != nil {
		t.Fatalf("HashHexString(taskB) error: %v", err)
	}

	if hashA == hashB {
		t.Fatalf("SECURITY VULNERABILITY: Hash collision on real Go structs! Both produced hash %s", hashA)
	}
}

func TestRFC8785_OfficialVectors(t *testing.T) {
	files := []string{"arrays.json", "french.json", "structures.json", "unicode.json", "values.json", "weird.json"}
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			inPath := filepath.Join("testdata", "rfc8785", "input", f)
			outPath := filepath.Join("testdata", "rfc8785", "output", f)

			inBytes, err := os.ReadFile(inPath)
			if err != nil {
				t.Fatalf("read input %s: %v", inPath, err)
			}
			wantBytes, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read output %s: %v", outPath, err)
			}

			gotBytes, err := Canonicalize(inBytes)
			if err != nil {
				t.Fatalf("Canonicalize(%s) error: %v", f, err)
			}

			if !bytes.Equal(gotBytes, wantBytes) {
				t.Fatalf("RFC 8785 vector %s mismatch:\n  got:  %s\n  want: %s", f, string(gotBytes), string(wantBytes))
			}
		})
	}
}

func FuzzCanonicalize(f *testing.F) {
	// Seed with valid and edge case inputs
	seeds := [][]byte{
		[]byte(`{"a":1,"b":"hello"}`),
		[]byte(`[1,2,3]`),
		[]byte(`{"msg":"cierre } falso","z":1}`),
		[]byte(`1e2`),
		[]byte(`-0`),
		[]byte(`null`),
		[]byte(`true`),
		[]byte(`false`),
		[]byte(`{"z":1,"a":2}`),
		[]byte(`{"peach":"This sorting order","péché":"is wrong according to French"}`),
	}

	// Add official vectors as seeds
	for _, fname := range []string{"arrays.json", "french.json", "structures.json", "unicode.json", "values.json", "weird.json"} {
		if b, err := os.ReadFile(filepath.Join("testdata", "rfc8785", "input", fname)); err == nil {
			seeds = append(seeds, b)
		}
	}

	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		c1, err := Canonicalize(data)
		if err != nil {
			// Malformed or non-compliant input is properly rejected.
			return
		}

		// 1. Idempotency: Canonicalize(Canonicalize(x)) == Canonicalize(x)
		c2, err := Canonicalize(c1)
		if err != nil {
			t.Fatalf("Idempotency failure: Canonicalize(c1) returned error: %v", err)
		}
		if !bytes.Equal(c1, c2) {
			t.Fatalf("Idempotency failure: c1 != c2\n  c1: %s\n  c2: %s", string(c1), string(c2))
		}

		// 2. Semantic equivalence: unmarshaling c1 must match unmarshaling data
		var vOriginal, vCanon any
		if errOrig := json.Unmarshal(data, &vOriginal); errOrig == nil {
			if errCanon := json.Unmarshal(c1, &vCanon); errCanon != nil {
				t.Fatalf("Canonical output failed to unmarshal: %v", errCanon)
			}
			if !reflect.DeepEqual(vOriginal, vCanon) {
				t.Fatalf("Semantic divergence:\n  original: %#v\n  canon:    %#v", vOriginal, vCanon)
			}
		}
	})
}
