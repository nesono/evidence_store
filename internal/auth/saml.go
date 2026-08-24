package auth

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"

	"github.com/nesono/evidence-store/internal/config"
)

// SAMLProvider is the second front end docs/rbac-design.md section 9 promised:
// same shape as OIDC, different protocol.
//
// It produces the same Claims the OIDC provider does, which is the whole point
// of having done RBAC first — everything from Principal inward cannot tell the
// two apart, and neither can the session, the roles, or the source binding.
type SAMLProvider struct {
	sp  saml.ServiceProvider
	cfg config.SAML
}

// NewSAMLProvider loads the service provider's keypair and the identity
// provider's metadata. Both are startup work on purpose: a missing key or an
// unreachable metadata URL should stop the server, not wait to be discovered by
// the first person trying to log in.
func NewSAMLProvider(ctx context.Context, cfg config.SAML) (*SAMLProvider, error) {
	keypair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load SAML keypair: %w", err)
	}
	keypair.Leaf, err = x509.ParseCertificate(keypair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse SAML certificate: %w", err)
	}
	key, ok := keypair.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		// crewjam/saml accepts any crypto.Signer, but every identity provider
		// worth naming expects RSA, and a key they cannot verify would only
		// fail later and less clearly.
		return nil, fmt.Errorf("SAML key must be RSA")
	}

	idpMetadata, err := loadIDPMetadata(ctx, cfg)
	if err != nil {
		return nil, err
	}

	root, err := url.Parse(cfg.RootURL)
	if err != nil {
		return nil, fmt.Errorf("parse EVIDENCE_SAML_ROOT_URL: %w", err)
	}
	metadataURL := root.JoinPath(SAMLMetadataPath)
	acsURL := root.JoinPath(SAMLACSPath)

	entityID := cfg.EntityID
	if entityID == "" {
		// The usual convention, and one less thing to get wrong at both ends.
		entityID = metadataURL.String()
	}

	return &SAMLProvider{
		cfg: cfg,
		sp: saml.ServiceProvider{
			EntityID:    entityID,
			Key:         key,
			Certificate: keypair.Leaf,
			MetadataURL: *metadataURL,
			AcsURL:      *acsURL,
			IDPMetadata: idpMetadata,
			// Ask for whatever the provider considers this person's usual name.
			// Demanding a specific format is how a working integration breaks
			// on a directory that spells identifiers differently.
			AuthnNameIDFormat: saml.UnspecifiedNameIDFormat,
		},
	}, nil
}

// Paths the routes are mounted at. They are constants because the metadata
// handed to the identity provider is built from them, and metadata that
// disagrees with the routes produces a login the provider misdelivers.
const (
	SAMLMetadataPath = "/auth/saml/metadata"
	SAMLLoginPath    = "/auth/saml/login"
	SAMLACSPath      = "/auth/saml/acs"
)

func loadIDPMetadata(ctx context.Context, cfg config.SAML) (*saml.EntityDescriptor, error) {
	if cfg.IDPMetadataFile != "" {
		raw, err := os.ReadFile(cfg.IDPMetadataFile)
		if err != nil {
			return nil, fmt.Errorf("read SAML IdP metadata: %w", err)
		}
		metadata, err := samlsp.ParseMetadata(raw)
		if err != nil {
			return nil, fmt.Errorf("parse SAML IdP metadata: %w", err)
		}
		return metadata, nil
	}

	parsed, err := url.Parse(cfg.IDPMetadataURL)
	if err != nil {
		return nil, fmt.Errorf("parse EVIDENCE_SAML_IDP_METADATA_URL: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	metadata, err := samlsp.FetchMetadata(ctx, client, *parsed)
	if err != nil {
		return nil, fmt.Errorf("fetch SAML IdP metadata from %s: %w", cfg.IDPMetadataURL, err)
	}
	return metadata, nil
}

// Metadata is what an administrator hands to the identity provider to register
// this store. Serving it rather than asking someone to write it by hand is what
// keeps the two ends agreeing about URLs and certificates.
func (p *SAMLProvider) Metadata() ([]byte, error) {
	xmlBytes, err := xml.MarshalIndent(p.sp.Metadata(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal SAML metadata: %w", err)
	}
	return append([]byte(xml.Header), xmlBytes...), nil
}

// AuthnRequest begins a login. The returned id is what the provider will echo
// back in InResponseTo, and the caller has to remember it: an assertion
// answering a request nobody made is one of the things a service provider is
// supposed to refuse.
func (p *SAMLProvider) AuthnRequest(relayState string) (redirect *url.URL, id string, err error) {
	binding := saml.HTTPRedirectBinding
	idpURL := p.sp.GetSSOBindingLocation(binding)
	if idpURL == "" {
		// Some providers only publish the POST binding. Falling back keeps
		// those working rather than failing on a technicality.
		binding = saml.HTTPPostBinding
		idpURL = p.sp.GetSSOBindingLocation(binding)
	}
	if idpURL == "" {
		return nil, "", fmt.Errorf("identity provider publishes no supported single sign-on binding")
	}

	req, err := p.sp.MakeAuthenticationRequest(idpURL, binding, saml.HTTPPostBinding)
	if err != nil {
		return nil, "", fmt.Errorf("build SAML authentication request: %w", err)
	}

	target, err := req.Redirect(relayState, &p.sp)
	if err != nil {
		return nil, "", fmt.Errorf("encode SAML authentication request: %w", err)
	}
	return target, req.ID, nil
}

// ParseAssertion validates the response the identity provider posted back —
// signature, conditions, audience, timing, and that its InResponseTo names a
// request we actually issued — and reads the person out of it.
//
// possibleRequestIDs is every login currently in flight. It is a list because
// the assertion arrives as a cross-site POST, which carries no cookie of ours
// to say which browser it belongs to; the signature and the request id together
// are what make it trustworthy.
func (p *SAMLProvider) ParseAssertion(r *http.Request, possibleRequestIDs []string) (*Claims, error) {
	assertion, err := p.sp.ParseResponse(r, possibleRequestIDs)
	if err != nil {
		// The library's error carries the private detail; the caller logs it
		// and tells the browser something less useful to an attacker.
		return nil, fmt.Errorf("validate SAML assertion: %w", err)
	}

	claims := &Claims{
		Issuer: p.sp.IDPMetadata.EntityID,
		Email:  p.attr(assertion, p.cfg.EmailAttribute, emailAttributeNames...),
		Name:   p.attr(assertion, p.cfg.NameAttribute, nameAttributeNames...),
		Groups: p.attrValues(assertion, p.cfg.GroupsAttribute, groupsAttributeNames...),
	}

	// The NameID is the provider's stable name for this person, and the
	// closest thing SAML has to OIDC's sub.
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		claims.Subject = strings.TrimSpace(assertion.Subject.NameID.Value)
	}
	if claims.Subject == "" {
		// Without one there is nothing to key a principal on, and matching on
		// the email address instead is exactly the mistake that splits somebody
		// in two when they are renamed.
		return nil, fmt.Errorf("SAML assertion carries no NameID")
	}
	return claims, nil
}

// Attribute names to try when the configured one is not present. Providers
// disagree, and these URNs are what Entra, ADFS and Shibboleth actually send —
// an operator should not have to discover that by reading a failed login.
var (
	emailAttributeNames = []string{
		"email", "mail", "emailAddress",
		"urn:oid:0.9.2342.19200300.100.1.3",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
	}
	nameAttributeNames = []string{
		"displayName", "name", "cn",
		"urn:oid:2.16.840.1.113730.3.1.241", // displayName
		"urn:oid:2.5.4.3",                   // cn, which is what Shibboleth sends
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
	}
	groupsAttributeNames = []string{
		"groups", "memberOf", "Group",
		"http://schemas.xmlsoap.org/claims/Group",
		"http://schemas.microsoft.com/ws/2008/06/identity/claims/groups",
	}
)

func (p *SAMLProvider) attr(assertion *saml.Assertion, configured string, fallbacks ...string) string {
	values := p.attrValues(assertion, configured, fallbacks...)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// attrValues reads an attribute by the configured name, then by the names
// providers commonly use. Matching on FriendlyName as well as Name is what
// makes "groups" find an attribute the provider sent as a URN.
func (p *SAMLProvider) attrValues(assertion *saml.Assertion, configured string, fallbacks ...string) []string {
	wanted := make([]string, 0, len(fallbacks)+1)
	if configured != "" {
		wanted = append(wanted, configured)
	}
	wanted = append(wanted, fallbacks...)

	for _, name := range wanted {
		for _, statement := range assertion.AttributeStatements {
			for _, attribute := range statement.Attributes {
				if !strings.EqualFold(attribute.Name, name) &&
					!strings.EqualFold(attribute.FriendlyName, name) {
					continue
				}
				values := make([]string, 0, len(attribute.Values))
				for _, v := range attribute.Values {
					if trimmed := strings.TrimSpace(v.Value); trimmed != "" {
						values = append(values, trimmed)
					}
				}
				if len(values) > 0 {
					return values
				}
			}
		}
	}
	return nil
}
