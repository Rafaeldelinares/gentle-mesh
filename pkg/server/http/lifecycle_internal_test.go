package http

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFailureCauseClassifies(t *testing.T) {
	cases := []struct {
		err   error
		code  string
		parte string
	}{
		{context.DeadlineExceeded, "TASK_TIMEOUT", "task timeout"},
		{context.Canceled, "TASK_CANCELED", "task canceled"},
		{errors.New("dial tcp: connection refused"), "DISPATCH_FAILED", "dispatch failed: "},
	}
	for _, tc := range cases {
		code, msg := failureCause(tc.err)
		if code != tc.code {
			t.Errorf("failureCause(%v).code = %s, want %s", tc.err, code, tc.code)
		}
		if !strings.Contains(msg, tc.parte) {
			t.Errorf("failureCause(%v).msg = %q, want it to contain %q", tc.err, msg, tc.parte)
		}
	}
}
