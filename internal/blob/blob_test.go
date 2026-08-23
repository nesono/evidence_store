package blob

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngBytes is a real encoded image rather than a magic-number prefix: the
// detector is the gate on what gets served back to a browser, so it is worth
// testing against something a browser would actually accept.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestDigestOfIsStableAndPrefixed(t *testing.T) {
	// The empty string's SHA-256 is a known constant, which pins both the
	// algorithm and the spelling.
	assert.Equal(t,
		Digest("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
		DigestOf(nil))
	assert.Equal(t, DigestOf([]byte("same")), DigestOf([]byte("same")))
	assert.NotEqual(t, DigestOf([]byte("a")), DigestOf([]byte("b")))
}

func TestParseDigestRejectsAnythingUnusable(t *testing.T) {
	valid := string(DigestOf([]byte("x")))
	got, err := ParseDigest(valid)
	require.NoError(t, err)
	assert.Equal(t, Digest(valid), got)

	// A digest that parses becomes an object key, so everything that is not
	// exactly lowercase hex of the right length has to be refused here.
	for _, bad := range []string{
		"",
		"sha256:",
		"deadbeef",
		"sha256:" + string(bytes.Repeat([]byte("f"), 63)),
		"sha256:" + string(bytes.Repeat([]byte("f"), 65)),
		"sha256:" + string(bytes.Repeat([]byte("F"), 64)),
		"sha512:" + string(bytes.Repeat([]byte("f"), 64)),
		"sha256:../../etc/passwd" + string(bytes.Repeat([]byte("f"), 42)),
		" " + valid,
		valid + "\n",
	} {
		_, err := ParseDigest(bad)
		assert.Error(t, err, "expected %q to be rejected", bad)
	}
}

func TestKeyRoundTripsThroughDigestFromKey(t *testing.T) {
	d := DigestOf([]byte("screenshot"))
	h := d.Hex()
	assert.Equal(t, "sha256/"+h[0:2]+"/"+h[2:4]+"/"+h, d.Key())

	got, ok := digestFromKey(d.Key())
	require.True(t, ok)
	assert.Equal(t, d, got)
}

func TestDigestFromKeyRejectsForeignKeys(t *testing.T) {
	h := DigestOf([]byte("x")).Hex()
	for _, bad := range []string{
		"",
		"staging/staging-123",
		"sha256/" + h,
		"md5/" + h[0:2] + "/" + h[2:4] + "/" + h,
		// Fan-out that disagrees with the hex would mean two keys for one blob.
		"sha256/zz/" + h[2:4] + "/" + h,
	} {
		_, ok := digestFromKey(bad)
		assert.False(t, ok, "expected %q to be rejected", bad)
	}
}

func TestDetectMediaAcceptsTheEmbeddableTypes(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
		media string
		ext   string
	}{
		{"png", pngBytes(t), "image/png", "png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), "image/jpeg", "jpg"},
		{"gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), "image/gif", "gif"},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00"), "image/webp", "webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media, ext, err := DetectMedia(tt.bytes)
			require.NoError(t, err)
			assert.Equal(t, tt.media, media)
			assert.Equal(t, tt.ext, ext)
		})
	}
}

func TestDetectMediaRefusesEverythingElse(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
	}{
		// Markup that calls itself an image is the case the allowlist exists
		// for: this renders as a document, and a document can carry script.
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"html", []byte("<!DOCTYPE html><html><body>hi</body></html>")},
		{"pdf", []byte("%PDF-1.7\n")},
		{"plain text", []byte("just a log line")},
		{"empty", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DetectMedia(tt.bytes)
			assert.ErrorIs(t, err, ErrUnsupportedMedia)
		})
	}
}

// The uploader's claim about its own bytes is not consulted anywhere, so a PNG
// announced as an SVG is stored and served as a PNG.
func TestDetectMediaIgnoresClaimedType(t *testing.T) {
	media, _, err := DetectMedia(pngBytes(t))
	require.NoError(t, err)
	assert.Equal(t, "image/png", media)
}

func TestRefPath(t *testing.T) {
	d := DigestOf([]byte("shot"))
	assert.Equal(t, "/api/v1/blobs/"+string(d)+".png", Ref{Digest: d, Ext: "png"}.Path())
	// A reference without an extension still resolves; only the renderer's
	// choice of element depends on the hint.
	assert.Equal(t, "/api/v1/blobs/"+string(d), Ref{Digest: d}.Path())
}
