package blob

import "regexp"

// refPattern matches a blob reference wherever it appears in a test log.
//
// It is anchored on the serving path rather than on markdown image syntax
// because the same reference has to be found in `![shot](...)`, in a bare link
// a tester typed by hand, and in metadata.photo_uris. What the reference is
// used for is the renderer's business; what it points at is this.
//
// The leading class is what keeps `https://elsewhere.example.com/api/v1/blobs/…`
// from reading as a reference into this store: a reference is relative, so the
// path may only be preceded by a delimiter, never by the tail of a host or
// another path. RE2 has no lookbehind, so the delimiter is consumed and the
// captures start at the digest.
var refPattern = regexp.MustCompile(
	`(?:^|[^\w.:/@-])` + regexp.QuoteMeta(PathPrefix) +
		`(` + algorithm + `:[0-9a-f]{64})(?:\.([a-z0-9]{1,5}))?`)

// Refs returns the blob references in a text, deduplicated, in the order they
// first appear.
//
// Deduplication is by digest and not by digest-plus-extension: the same image
// written once as `.png` and once bare is one blob, and the reachability sweep
// counts blobs.
func Refs(text string) []Ref {
	var refs []Ref
	seen := make(map[Digest]bool)
	for _, m := range refPattern.FindAllStringSubmatch(text, -1) {
		d := Digest(m[1])
		if seen[d] {
			continue
		}
		seen[d] = true
		refs = append(refs, Ref{Digest: d, Ext: m[2]})
	}
	return refs
}
