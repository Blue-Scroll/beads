package main

import (
	"errors"
	"strings"
	"testing"
)

// TestUnresolvableDepTargetMessage grades the one refusal every "bd dep add"
// door shares (vn-wzy43u5). The end-to-end proof lives in
// dep_foreign_target_embedded_test.go, which needs an embedded Dolt; this one
// is pure Go so the fork's compile-and-smoke gate can run it.
func TestUnresolvableDepTargetMessage(t *testing.T) {
	msg := unresolvableDepTargetError("vn-lo9ubyx", "hq-c9mgk6", errors.New("no issue found")).Error()

	// A reader has to be able to tell which edge was refused, and to find the
	// one shape that does work. Both ids and the external: form are the whole
	// point of the text.
	for _, want := range []string{"vn-lo9ubyx", "hq-c9mgk6", "external:<project>:<capability>", "no issue found"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal should contain %q, got:\n%s", want, msg)
		}
	}

	// Nothing was written, so the text must not read like a success or offer
	// a way to force one. A flag added later without rewriting this text is
	// how a refusal quietly turns back into the bug.
	for _, unwanted := range []string{"Added", "--force", "--allow"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("refusal must not contain %q, got:\n%s", unwanted, msg)
		}
	}
}
