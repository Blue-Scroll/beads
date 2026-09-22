package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestUnresolvableCreateDepTargetMessage grades the one refusal every
// "bd create --deps" door shares (vn-zc0gvqh). The end-to-end proof lives in
// create_foreign_dep_embedded_test.go, which needs an embedded Dolt; this one
// is pure Go so the fork's compile-and-smoke gate can run it.
func TestUnresolvableCreateDepTargetMessage(t *testing.T) {
	msg := unresolvableCreateDepTargetError("hq-c9mgk6", errors.New("no issue found")).Error()

	// A reader has to be able to tell which target was refused, that nothing
	// was created, and where the one shape that works lives.
	for _, want := range []string{"hq-c9mgk6", "Nothing was created", "external:<project>:<capability>", "no issue found"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal should contain %q, got:\n%s", want, msg)
		}
	}

	// The advice has to name the CREATE door. A copy of dep add's text would
	// send the reader to a command that cannot help them here.
	if !strings.Contains(msg, "bd create ... --deps depends-on:external:") {
		t.Errorf("refusal should show the --deps external form, got:\n%s", msg)
	}

	// Nothing was written, so the text must not read like a success or offer
	// a way to force one. A flag added later without rewriting this text is
	// how a refusal quietly turns back into the bug.
	for _, unwanted := range []string{"Created issue", "--force", "--allow"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("refusal must not contain %q, got:\n%s", unwanted, msg)
		}
	}
}

// TestRequireResolvableCreateDepTargetsNilStore pins the stand-down. `bd
// create --repo=<remote URL>` from a directory with no local store leaves the
// store nil, and refusing every --deps there would break a command that works
// today.
func TestRequireResolvableCreateDepTargetsNilStore(t *testing.T) {
	specs, err := parseDepSpecs([]string{"blocked-by:zz-nosuchid"})
	if err != nil {
		t.Fatalf("parseDepSpecs: %v", err)
	}
	if err := requireResolvableCreateDepTargets(context.Background(), nil, specs); err != nil {
		t.Errorf("a nil store should stand down, got: %v", err)
	}
}
