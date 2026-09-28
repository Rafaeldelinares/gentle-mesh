//go:build redteam

package jcs

import (
	"bytes"
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
