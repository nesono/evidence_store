package blob

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRefsFindsReferencesWhereverTheyAppear(t *testing.T) {
	a := DigestOf([]byte("a"))
	b := DigestOf([]byte("b"))

	log := "## Run 2\n" +
		"1. Powered on the rig\n" +
		"![rig after step 3](" + Ref{Digest: a, Ext: "png"}.Path() + ")\n" +
		"Full size: " + Ref{Digest: b, Ext: "jpg"}.Path() + "\n"

	assert.Equal(t, []Ref{{Digest: a, Ext: "png"}, {Digest: b, Ext: "jpg"}}, Refs(log))
}

// One blob referenced twice is one blob. The sweep counts what is reachable,
// not how often a log mentions it.
func TestRefsDeduplicatesByDigest(t *testing.T) {
	d := DigestOf([]byte("shot"))
	log := "![first](" + Ref{Digest: d, Ext: "png"}.Path() + ")\n" +
		"and again, without the hint: " + Ref{Digest: d}.Path() + "\n"

	assert.Equal(t, []Ref{{Digest: d, Ext: "png"}}, Refs(log))
}

func TestRefsIgnoresEverythingThatIsNotOurs(t *testing.T) {
	h := DigestOf([]byte("x")).Hex()

	assert.Empty(t, Refs(""))
	assert.Empty(t, Refs("no images here, just a note about sha256 digests"))
	// An absolute URL to someone else's host is not a reference into this
	// store, and treating it as one would put a foreign digest in photo_uris.
	assert.Empty(t, Refs("![x](https://example.com/api/v1/blobs/sha256:"+h+".png)"))
	assert.Empty(t, Refs("![x](/api/v2/blobs/sha256:"+h+".png)"))
	assert.Empty(t, Refs("![x](/api/v1/blobs/sha256:deadbeef.png)"))
	assert.Empty(t, Refs("![x](/api/v1/blobs/SHA256:"+h+".png)"))
}

// An https:// prefix would otherwise be matched by the path fragment inside it.
func TestRefsDoesNotMatchInsideAnAbsoluteURL(t *testing.T) {
	d := DigestOf([]byte("x"))
	log := "see https://evil.example.com/api/v1/blobs/" + string(d) + ".png for the real one"
	assert.Empty(t, Refs(log))
}
