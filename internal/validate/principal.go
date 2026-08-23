package validate

import (
	"fmt"
	"strings"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/model"
)

// maxSubjectLen is a sanity bound, not a policy. A subject is a name somebody
// reads in an audit trail; anything this long is a mistake or an attempt to
// make a log unreadable.
const maxSubjectLen = 255

// PrincipalCreate checks a new identity before it is minted a credential.
func PrincipalCreate(c *model.PrincipalCreate) []string {
	var errs []string

	if strings.TrimSpace(c.Subject) == "" {
		errs = append(errs, "subject is required")
	} else if len(c.Subject) > maxSubjectLen {
		errs = append(errs, fmt.Sprintf("subject must be at most %d characters", maxSubjectLen))
	}
	if strings.ContainsAny(c.Subject, "\n\r\t") {
		errs = append(errs, "subject must not contain control characters")
	}

	switch c.Kind {
	case model.PrincipalKindAPIKey:
	case model.PrincipalKindUser:
		// A human principal has no key to authenticate with until there is a
		// login flow to produce one, so creating one here would issue an
		// identity that nothing can use. The SSO callback is what creates
		// these; see docs/rbac-design.md section 9.
		errs = append(errs, fmt.Sprintf("kind %q cannot be created here: user principals are created by logging in",
			c.Kind))
	default:
		errs = append(errs, fmt.Sprintf("kind %q is invalid, must be %s",
			c.Kind, model.PrincipalKindAPIKey))
	}

	errs = append(errs, Roles(c.Roles)...)
	return errs
}

// Roles checks a set of role names against the roles this binary defines.
//
// Naming the four in the message matters more than naming the rule: a caller
// sending "readonly" learns nothing from being told it is not a role, and every
// value rejected here is a client that has to be changed to send another.
func Roles(roles []string) []string {
	var errs []string
	seen := make(map[string]struct{}, len(roles))
	for _, name := range roles {
		if _, dup := seen[name]; dup {
			errs = append(errs, fmt.Sprintf("role %q is listed twice", name))
			continue
		}
		seen[name] = struct{}{}
		if _, ok := auth.ParseRole(name); !ok {
			errs = append(errs, fmt.Sprintf("role %q is invalid, must be one of %s",
				name, strings.Join(auth.RoleNames(), ", ")))
		}
	}
	return errs
}
