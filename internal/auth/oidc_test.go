package auth

import "testing"

// What Entra actually sends.
//
// A cloud-only Entra account created in the portal has no mail attribute, so no
// email claim is issued however the scopes are set — the store saw exactly this
// against a real tenant (#152). Falling straight through to sub stamped every
// record that person filed with an opaque identifier, which is not a name
// anybody reading the evidence months later can act on.
func TestSubjectFallsBackToTheLoginNameNotTheOpaqueOne(t *testing.T) {
	entra := Claims{
		Subject:           "oLArAQCryDWlzkmgTAetr1TCXViuqXyXQGgOQLjv3p8",
		PreferredUsername: "alice@contoso.onmicrosoft.com",
		Name:              "Alice",
	}
	if got, want := entra.PrincipalSubject(), "user:alice@contoso.onmicrosoft.com"; got != want {
		t.Errorf("PrincipalSubject() = %q, want %q", got, want)
	}
}

// An address still wins where there is one: it is the more useful of the two,
// and it is what the store has always filed evidence under.
func TestAnEmailStillOutranksTheLoginName(t *testing.T) {
	both := Claims{
		Subject:           "sub-123",
		Email:             "alice@example.com",
		PreferredUsername: "alice@contoso.onmicrosoft.com",
	}
	if got, want := both.PrincipalSubject(), "user:alice@example.com"; got != want {
		t.Errorf("PrincipalSubject() = %q, want %q", got, want)
	}
}

// A provider that offers neither leaves the opaque subject, which is ugly but
// resolvable — and better than inventing something that looks like an address
// and is not.
func TestWithNoNameAtAllTheSubjectIsStillSomething(t *testing.T) {
	bare := Claims{Subject: "sub-123"}
	if got, want := bare.PrincipalSubject(), "user:sub-123"; got != want {
		t.Errorf("PrincipalSubject() = %q, want %q", got, want)
	}
}
