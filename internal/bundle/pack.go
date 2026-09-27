package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// Bundle is a spec's files as pushed: the entry file plus its $ref closure,
// with their original bytes. Paths are slash paths relative to the
// descriptor's directory.
type Bundle struct {
	Entry string
	Files map[string][]byte // includes Entry
}

// ManifestPath is where the manifest sits in a packed bundle. Spec files
// may not live under .portal/, so it can't clash with one.
const ManifestPath = ".portal/manifest.json"

// Size limits for unpacking, after decompression.
var (
	maxFileSize  int64 = 10 << 20
	maxTotalSize int64 = 50 << 20
)

type manifest struct {
	Format      int            `json:"format"`
	Entry       string         `json:"entry"`
	ContentHash string         `json:"content_hash"`
	Files       []manifestFile `json:"files"`
}

type manifestFile struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"` // of the raw bytes
}

// Read reads files, relative to root, into a bundle. The first file is the
// entry, as in model.Spec.Files.
func Read(root string, files []string) (*Bundle, error) {
	if len(files) == 0 {
		return nil, errors.New("a bundle needs at least the entry file")
	}
	b := &Bundle{Entry: files[0], Files: map[string][]byte{}}
	for _, f := range files {
		if err := checkPath(f); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		b.Files[f] = data
	}
	return b, nil
}

// checkPath accepts clean, relative slash paths that stay inside the root
// and outside .portal/.
func checkPath(p string) error {
	switch {
	case p == "", strings.Contains(p, `\`), path.IsAbs(p), path.Clean(p) != p,
		p == "..", strings.HasPrefix(p, "../"):
		return fmt.Errorf("bundle path %q is not a clean relative path", p)
	case p == ".portal" || strings.HasPrefix(p, ".portal/"):
		return fmt.Errorf("bundle path %q: .portal/ is reserved for the manifest", p)
	}
	return nil
}

func (b *Bundle) paths() []string {
	ps := make([]string, 0, len(b.Files))
	for p := range b.Files {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

// Hash is the content hash: sha256 over the entry path, then each file's
// path and canonical JSON in path order. Canonical JSON is the parsed
// YAML or JSON re-encoded with sorted keys, so comments, formatting and key
// order don't count; paths do.
func (b *Bundle) Hash() (string, error) {
	h := sha256.New()
	io.WriteString(h, b.Entry+"\x00")
	for _, p := range b.paths() {
		doc, err := yamldoc.Parse(p, b.Files[p])
		if err != nil {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(doc.JSON()); err != nil {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		io.WriteString(h, p+"\x00")
		h.Write(buf.Bytes())
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (b *Bundle) manifest() (*manifest, error) {
	hash, err := b.Hash()
	if err != nil {
		return nil, err
	}
	m := &manifest{Format: 1, Entry: b.Entry, ContentHash: hash}
	for _, p := range b.paths() {
		sum := sha256.Sum256(b.Files[p])
		m.Files = append(m.Files, manifestFile{Path: p, Size: len(b.Files[p]), SHA256: hex.EncodeToString(sum[:])})
	}
	return m, nil
}

// Pack writes the bundle as tar.zst: the manifest, then the files in path
// order, with fixed metadata, so the same bundle always packs to the same
// bytes.
func (b *Bundle) Pack(w io.Writer) error {
	m, err := b.manifest()
	if err != nil {
		return err
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	put := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := put(ManifestPath, append(mb, '\n')); err != nil {
		return err
	}
	for _, p := range b.paths() {
		if err := put(p, b.Files[p]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// Unpack reads a packed bundle and checks it against its manifest: every
// file listed and nothing else, each file's sha256, and the content hash.
// Paths must be clean and relative, entries regular files, and sizes within
// limits.
func Unpack(r io.Reader) (*Bundle, error) {
	zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	tr := tar.NewReader(io.LimitReader(zr, maxTotalSize+1<<20)) // room for tar headers
	var m *manifest
	b := &Bundle{Files: map[string][]byte{}}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading bundle: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("bundle entry %q is not a regular file", h.Name)
		}
		if h.Size > maxFileSize {
			return nil, fmt.Errorf("bundle entry %q is larger than %d bytes", h.Name, maxFileSize)
		}
		if total += h.Size; total > maxTotalSize {
			return nil, fmt.Errorf("bundle is larger than %d bytes", maxTotalSize)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading bundle entry %q: %w", h.Name, err)
		}
		if m == nil {
			if h.Name != ManifestPath {
				return nil, fmt.Errorf("bundle starts with %q, not %s", h.Name, ManifestPath)
			}
			m = &manifest{}
			if err := json.Unmarshal(data, m); err != nil {
				return nil, fmt.Errorf("bundle manifest: %w", err)
			}
			if m.Format != 1 {
				return nil, fmt.Errorf("bundle manifest format %d is not supported", m.Format)
			}
			continue
		}
		if err := checkPath(h.Name); err != nil {
			return nil, err
		}
		if _, dup := b.Files[h.Name]; dup {
			return nil, fmt.Errorf("bundle has %q twice", h.Name)
		}
		b.Files[h.Name] = data
	}
	if m == nil {
		return nil, errors.New("bundle is empty")
	}
	if len(m.Files) != len(b.Files) {
		return nil, fmt.Errorf("bundle has %d files, its manifest lists %d", len(b.Files), len(m.Files))
	}
	for _, f := range m.Files {
		data, ok := b.Files[f.Path]
		if !ok {
			return nil, fmt.Errorf("bundle manifest lists %q, which is missing", f.Path)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return nil, fmt.Errorf("bundle file %q doesn't match its manifest sha256", f.Path)
		}
	}
	if _, ok := b.Files[m.Entry]; !ok {
		return nil, fmt.Errorf("bundle entry %q is not among its files", m.Entry)
	}
	b.Entry = m.Entry
	hash, err := b.Hash()
	if err != nil {
		return nil, err
	}
	if hash != m.ContentHash {
		return nil, fmt.Errorf("bundle content hash is %s, its manifest says %s", hash, m.ContentHash)
	}
	return b, nil
}

// WriteDir writes the bundle's files under dir, for parsers that read from
// disk.
func (b *Bundle) WriteDir(dir string) error {
	for _, p := range b.paths() {
		if err := checkPath(p); err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b.Files[p], 0o644); err != nil {
			return err
		}
	}
	return nil
}
