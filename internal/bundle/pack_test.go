package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func sample() *Bundle {
	return &Bundle{Entry: "api/openapi.yaml", Files: map[string][]byte{
		"api/openapi.yaml":       []byte("openapi: 3.1.0\ninfo: {title: T, version: 1.0.0}\ncomponents:\n  schemas:\n    M: {$ref: 'schemas/money.json'}\n"),
		"api/schemas/money.json": []byte(`{"type": "object", "properties": {"amount": {"type": "integer"}}}`),
	}}
}

func pack(t *testing.T, b *Bundle) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := b.Pack(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func hash(t *testing.T, b *Bundle) string {
	t.Helper()
	h, err := b.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPackRoundTrip(t *testing.T) {
	b := sample()
	packed := pack(t, b)
	if !bytes.Equal(packed, pack(t, sample())) {
		t.Error("packing the same bundle twice gave different bytes")
	}
	got, err := Unpack(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Errorf("round trip = %+v, want %+v", got, b)
	}
}

func TestReadAndWriteDir(t *testing.T) {
	dir := t.TempDir()
	if err := sample().WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	b, err := Read(dir, []string{"api/openapi.yaml", "api/schemas/money.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b, sample()) {
		t.Errorf("read = %+v", b)
	}
	if _, err := Read(dir, []string{"../x.yaml"}); err == nil {
		t.Error("Read accepted a path outside the root")
	}
	bad := &Bundle{Entry: "../x", Files: map[string][]byte{"../x": nil}}
	if err := bad.WriteDir(dir); err == nil {
		t.Error("WriteDir accepted a path outside the directory")
	}
}

func TestHashIgnoresFormatting(t *testing.T) {
	base := hash(t, sample())
	same := map[string]string{
		"comments and whitespace": "# the API\nopenapi:   3.1.0   # version\n\ninfo: {title: T, version: 1.0.0}\ncomponents:\n  schemas:\n    M: {$ref: 'schemas/money.json'}\n",
		"key order":               "info: {version: 1.0.0, title: T}\nopenapi: 3.1.0\ncomponents:\n  schemas:\n    M: {$ref: 'schemas/money.json'}\n",
		"block style":             "openapi: 3.1.0\ninfo:\n  title: T\n  version: 1.0.0\ncomponents:\n  schemas:\n    M:\n      $ref: schemas/money.json\n",
	}
	for name, doc := range same {
		b := sample()
		b.Files["api/openapi.yaml"] = []byte(doc)
		if h := hash(t, b); h != base {
			t.Errorf("%s changed the hash", name)
		}
	}
}

func TestHashChanges(t *testing.T) {
	base := hash(t, sample())

	value := sample()
	value.Files["api/schemas/money.json"] = []byte(`{"type": "object", "properties": {"amount": {"type": "number"}}}`)

	renamed := sample()
	renamed.Files["api/schemas/cash.json"] = renamed.Files["api/schemas/money.json"]
	delete(renamed.Files, "api/schemas/money.json")

	entry := sample()
	entry.Entry = "api/schemas/money.json"

	for name, b := range map[string]*Bundle{"a value": value, "a file name": renamed, "the entry": entry} {
		if hash(t, b) == base {
			t.Errorf("changing %s kept the hash", name)
		}
	}
}

// rawEntry is one tar entry for building tampered bundles.
type rawEntry struct {
	name     string
	data     []byte
	typeflag byte
}

func rawPack(t *testing.T, entries []rawEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tf}
		if tf == tar.TypeSymlink {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if tf == tar.TypeReg {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// entriesOf returns sample's valid entries, after edit has changed its
// manifest.
func entriesOf(t *testing.T, edit func(m *manifest)) []rawEntry {
	t.Helper()
	b := sample()
	m, err := b.manifest()
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(m)
	}
	mb, _ := json.Marshal(m)
	es := []rawEntry{{name: ManifestPath, data: mb}}
	for _, p := range b.paths() {
		es = append(es, rawEntry{name: p, data: b.Files[p]})
	}
	return es
}

func listed(path string, data []byte) func(m *manifest) {
	return func(m *manifest) {
		sum := sha256.Sum256(data)
		m.Files = append(m.Files, manifestFile{Path: path, Size: len(data), SHA256: hex.EncodeToString(sum[:])})
	}
}

func TestUnpackRejects(t *testing.T) {
	evil := []byte("type: string\n")
	tests := map[string]struct {
		entries []rawEntry
		want    string
	}{
		"no manifest":  {entriesOf(t, nil)[1:], "not " + ManifestPath},
		"empty":        {nil, "empty"},
		"format":       {entriesOf(t, func(m *manifest) { m.Format = 2 }), "format 2"},
		"parent path":  {append(entriesOf(t, listed("../x.yaml", evil)), rawEntry{name: "../x.yaml", data: evil}), "clean relative"},
		"absolute":     {append(entriesOf(t, listed("/x.yaml", evil)), rawEntry{name: "/x.yaml", data: evil}), "clean relative"},
		"reserved":     {append(entriesOf(t, listed(".portal/x.yaml", evil)), rawEntry{name: ".portal/x.yaml", data: evil}), "reserved"},
		"duplicate":    {append(entriesOf(t, nil), rawEntry{name: "api/openapi.yaml", data: evil}), "twice"},
		"symlink":      {append(entriesOf(t, nil), rawEntry{name: "api/link.yaml", typeflag: tar.TypeSymlink}), "not a regular file"},
		"unlisted":     {append(entriesOf(t, nil), rawEntry{name: "api/extra.yaml", data: evil}), "manifest lists 2"},
		"missing":      {entriesOf(t, listed("api/gone.yaml", evil)), "has 2 files"},
		"sha mismatch": {entriesOf(t, func(m *manifest) { m.Files[0].SHA256 = strings.Repeat("0", 64) }), "sha256"},
		"hash":         {entriesOf(t, func(m *manifest) { m.ContentHash = "sha256:00" }), "content hash"},
		"entry":        {entriesOf(t, func(m *manifest) { m.Entry = "api/nope.yaml" }), "not among its files"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Unpack(bytes.NewReader(rawPack(t, tt.entries)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestUnpackSizeLimits(t *testing.T) {
	defer func(f, total int64) { maxFileSize, maxTotalSize = f, total }(maxFileSize, maxTotalSize)
	packed := pack(t, sample())

	maxFileSize = 32
	if _, err := Unpack(bytes.NewReader(packed)); err == nil || !strings.Contains(err.Error(), "larger than 32") {
		t.Errorf("file limit: err = %v", err)
	}
	maxFileSize, maxTotalSize = 1<<20, 200
	if _, err := Unpack(bytes.NewReader(packed)); err == nil || !strings.Contains(err.Error(), "bundle is larger") {
		t.Errorf("total limit: err = %v", err)
	}
}

func TestPackRejectsUnparseableFiles(t *testing.T) {
	b := sample()
	b.Files["api/schemas/money.json"] = []byte("{not: [valid")
	if err := b.Pack(&bytes.Buffer{}); err == nil {
		t.Error("packed a file that isn't YAML or JSON")
	}
}
