package auth

import (
	"context"
	"errors"
	"fmt"
)

// ErrSourceNotOwned means the caller tried to file evidence under a name that
// is not theirs and does not hold the permission to do so.
var ErrSourceNotOwned = errors.New("source does not match the caller's identity")

// BindSource decides what a record's source may say, given who is filing it.
//
// A record's source is what a reader goes on months later to ask who ran this
// and whether to believe them. Until now it was free text the client set to
// whatever it liked, which made it a label rather than an attribution —
// DESIGN.md section 8 has always wanted human tokens pinned to their own
// username, and nothing enforced it.
//
// The rule, in the order it is applied:
//
//   - No principal at all: unchanged. The store runs open in local development,
//     and there is no identity to pin anything to.
//   - Caller holds source:any — that is, ci: taken as sent. A build robot
//     legitimately writes a source that is not its own name, because the useful
//     attribution is the build URL, not the robot.
//   - Empty: filled in with the caller's subject. Saying nothing is not a claim
//     about anybody, so the server makes the true one.
//   - Equal to the caller's subject: taken as sent.
//   - Anything else: ErrSourceNotOwned.
//
// This runs before validation, so a filled-in subject satisfies the
// source-is-required rule the validator applies. An anonymous caller sending
// nothing still fails there, exactly as before.
func BindSource(ctx context.Context, source string) (string, error) {
	principal, ok := PrincipalFrom(ctx)
	if !ok {
		return source, nil
	}
	if principal.Can(PermSourceAny) {
		return source, nil
	}
	if source == "" {
		return principal.Subject, nil
	}
	if source == principal.Subject {
		return source, nil
	}
	// Naming the expected value gives away nothing — it is the caller's own
	// subject — and saves them guessing what the server wanted.
	return "", fmt.Errorf("%w: %q may only write evidence with source %q",
		ErrSourceNotOwned, principal.Subject, principal.Subject)
}
