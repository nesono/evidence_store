package campaign

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const (
	MaxArchiveBytes     = 16 << 20
	MaxExpandedBytes    = 64 << 20
	MaxPackageFileBytes = 8 << 20
	MaxPackageEntries   = 2048
)

// ReadPackage checks the entire gzip stream, archive paths, definitions, and
// declared dependencies. Nothing is extracted to disk or fetched from a URL.
func ReadPackage(input io.Reader) (*Package, error) {
	p, _, err := ReadPackageAssets(input)
	return p, err
}

// ReadPackageAssets returns validated setup-asset bytes keyed by digest for CAS
// ingestion. The returned map contains only declared assets, never arbitrary
// archive entries; shared assets appear once.
func ReadPackageAssets(input io.Reader) (*Package, map[string][]byte, error) {
	data, err := readBounded(input, MaxArchiveBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read archive: %w", err)
	}
	files, err := unpack(data)
	if err != nil {
		return nil, nil, err
	}
	p, err := validateFiles(files)
	if err != nil {
		return nil, nil, err
	}
	p.Digest = contentDigest(data)
	assets := make(map[string][]byte)
	for _, scenario := range p.Scenarios {
		for name, digest := range scenario.Assets {
			assets[digest] = files[name]
		}
	}
	return p, assets, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return data, nil
}

func unpack(data []byte) (map[string][]byte, error) {
	compressed := bytes.NewReader(data)
	z, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, fmt.Errorf("gzip header: %w", err)
	}
	z.Multistream(false)
	expanded, readErr := readBounded(z, MaxExpandedBytes)
	closeErr := z.Close()
	if readErr != nil {
		return nil, fmt.Errorf("gzip data: %w", readErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if compressed.Len() != 0 {
		return nil, fmt.Errorf("trailing data or multiple gzip members are not supported")
	}
	return readTar(expanded)
}

func readTar(data []byte) (map[string][]byte, error) {
	raw := bytes.NewReader(data)
	r := tar.NewReader(raw)
	files := map[string][]byte{}
	seen := map[string]bool{}
	for {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar header: %w", err)
		}
		name, err := validateHeader(header, seen)
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		files[name], err = readBounded(r, MaxPackageFileBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	// Reject hidden data after the tar terminator, including a second archive.
	if bytes.Count(data[len(data)-raw.Len():], []byte{0}) != raw.Len() {
		return nil, fmt.Errorf("nonzero trailing tar data")
	}
	for name := range seen {
		if err := validateAncestors(name, files); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func validateHeader(h *tar.Header, seen map[string]bool) (string, error) {
	name := h.Name
	if h.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	if !archivePath(name) || seen[name] {
		return "", fmt.Errorf("invalid or duplicate archive path %q", h.Name)
	}
	if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
		return "", fmt.Errorf("%s: only regular files and directories are supported", name)
	}
	if len(h.PAXRecords) != 0 || h.Linkname != "" {
		return "", fmt.Errorf("%s: extended metadata and links are not supported", name)
	}
	if h.Size < 0 || h.Size > MaxPackageFileBytes || (h.Typeflag == tar.TypeDir && h.Size != 0) {
		return "", fmt.Errorf("%s: invalid or oversized entry", name)
	}
	if len(seen) >= MaxPackageEntries {
		return "", fmt.Errorf("archive exceeds %d entries", MaxPackageEntries)
	}
	seen[name] = true
	return name, nil
}

func validateAncestors(name string, files map[string][]byte) error {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[:i], "/")
		if _, exists := files[parent]; exists {
			return fmt.Errorf("%s: parent %s is a file", name, parent)
		}
	}
	return nil
}

func contentDigest(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// BuildPackage consumes an explicit map of archive-relative files. It preserves
// their bytes and uses sorted paths, fixed metadata, and fixed compression. The
// same declared inputs and Go toolchain produce the same bytes and digest.
func BuildPackage(files map[string][]byte) ([]byte, *Package, error) {
	p, err := validateFiles(files)
	if err != nil {
		return nil, nil, err
	}
	var output bytes.Buffer
	z, err := gzip.NewWriterLevel(&output, gzip.BestCompression)
	if err != nil {
		return nil, nil, err
	}
	if err := writeTar(z, files); err != nil {
		_ = z.Close() // Preserve the earlier archive error.
		return nil, nil, err
	}
	if err := z.Close(); err != nil {
		return nil, nil, err
	}
	if output.Len() > MaxArchiveBytes {
		return nil, nil, fmt.Errorf("compressed archive exceeds %d bytes", MaxArchiveBytes)
	}
	data := output.Bytes()
	p.Digest = contentDigest(data)
	return data, p, nil
}

func writeTar(output io.Writer, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	w := tar.NewWriter(output)
	for _, name := range names {
		h := &tar.Header{Name: name, Mode: 0644, Size: int64(len(files[name])), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := w.WriteHeader(h); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := w.Write(files[name]); err != nil {
			return err
		}
	}
	return w.Close()
}
