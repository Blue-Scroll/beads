package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/issueops"
)

// unresolvableCreateDepTargetError is the one refusal every "bd create --deps"
// door gives when a target is an id this database cannot read and is not an
// external: reference.
//
// It is the create-time twin of unresolvableDepTargetError in dep.go, which
// says the same thing for "bd dep add". Keep the two in step: a door that
// writes a dependency edge must refuse the same targets with the same reason,
// or the shape agents are pushed through hardest becomes the one hole left.
//
// Why a refusal and not a stored edge. bd files a different-prefix target in
// dependencies.depends_on_external, and nothing joins that column back to a
// real row, so the three readers disagree about the same edge:
//
//	dependency_count  counts the row                  -> 1
//	bd show           joins issues and drops the row  -> no Dependencies
//	bd blocked/ready  the is_blocked union has no leg
//	                  for depends_on_external         -> still ready
//
// So the edge reads as a fence to whoever filed it and reads as ready to
// everything that hands out work. vn-wzy43u5 shut that door on "bd dep add"
// and left this one open; 20 such rows were cleared out of the town's ledgers
// on 2026-09-22, and every polecat's boot policy tells it to file follow-up
// work with exactly "bd create --deps blocked-by:<id>" (vn-zc0gvqh).
func unresolvableCreateDepTargetError(target string, cause error) error {
	return fmt.Errorf(`cannot create with --deps %s: no issue with that id is in this database, and %q is not an "external:" reference (%v)

Nothing was created. bd has no way to store this edge so that it holds: a
different-prefix id goes into a column no reader joins back to a row, so
dependency_count would read 1, bd show would list no dependency, and bd ready
would still hand the issue out.

If the target is a capability in another project, say so:
  bd create ... --deps depends-on:external:<project>:<capability>

If the blocker only exists in another database, this tool cannot fence on it.
Create the issue with no edge, hold it (a hold label your pool honors, or
bd defer), and write the blocker id in a note.`, target, target, cause)
}

// requireResolvableCreateDepTargets refuses a --deps target this database
// cannot read, BEFORE anything is created. Running it early is the point: the
// create path burns a child ID and writes the issue first, so a late refusal
// would leave an issue behind with the fence its filer asked for missing.
//
// It checks every spec, in both directions. A "blocks:" spec makes the target
// the source of the edge, which fails later anyway when the source lookup
// misses; failing here instead costs nothing and says why.
//
// st is the store the create will actually write to (callers must pass the
// one they resolved for --repo, not the process's first store). A nil store
// means there is nothing to resolve against, so the check stands down rather
// than refusing everything.
func requireResolvableCreateDepTargets(ctx context.Context, st storage.DoltStorage, specs []domain.DependencySpec) error {
	if st == nil {
		return nil
	}
	for _, spec := range specs {
		if IsExternalRef(spec.TargetID) {
			continue
		}
		_, _, cleanup, err := resolveIDWithRouting(ctx, st, spec.TargetID)
		if err != nil {
			return unresolvableCreateDepTargetError(spec.TargetID, err)
		}
		cleanup()
	}
	return nil
}

// requireProxiedCreateDepTargets is requireResolvableCreateDepTargets for the
// proxied-server door, which reads through the proxied Reader role instead of
// a store. It mirrors requireProxiedDepTarget in dep_proxied_server.go. The
// reader's Get spans both planes: a miss on the issue AND the wisp table is
// ErrNotFound, and a backend failure passes through unchanged, so only a real
// absence is refused.
func requireProxiedCreateDepTargets(ctx context.Context, specs []domain.DependencySpec) error {
	var rd issueops.Reader
	for _, spec := range specs {
		if IsExternalRef(spec.TargetID) {
			continue
		}
		if rd == nil {
			var err error
			if rd, err = proxiedIssueReader(); err != nil {
				return err
			}
		}
		if _, err := rd.Get(ctx, issueops.GetRequest{ID: spec.TargetID}); err == nil {
			continue
		} else if !errors.Is(err, issueops.ErrNotFound) {
			return err
		}
		return unresolvableCreateDepTargetError(spec.TargetID, errors.New("not found in this database"))
	}
	return nil
}
