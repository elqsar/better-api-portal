package check

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/eventcatalog"
	"better-api-portal/internal/spec/openapi"
	"better-api-portal/internal/yamldoc"
)

// DescriptorName is the file name uploaded descriptors are checked under,
// and so the file findings about the descriptor point at.
const DescriptorName = "portal.yaml"

// RunBundles checks an uploaded descriptor whose specs arrive as bundles, one
// per API id, the way the server receives a push. It lays them out in a
// temporary directory as they were in the repo and runs Run there, so the
// result is the one `portal check` gives for the same files. Paths in the
// report are relative to the descriptor's directory.
//
// A push whose bundles don't match the descriptor, one per API with the
// declared spec as the entry, is an error rather than a finding: the client
// built it wrong, and the checkout it came from may be fine.
func RunBundles(desc []byte, bundles map[string]*bundle.Bundle, opts Options) (*Report, error) {
	tmp, err := os.MkdirTemp("", "portal-push-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	root := filepath.Join(tmp, "src")
	if err := os.Mkdir(root, 0o755); err != nil {
		return nil, err
	}
	descPath := filepath.Join(root, DescriptorName)
	if err := os.WriteFile(descPath, desc, 0o644); err != nil {
		return nil, err
	}
	if err := writeBundles(root, bundles); err != nil {
		return nil, err
	}
	d, _, err := descriptor.Load(descPath)
	if err != nil {
		return nil, err
	}
	if d != nil {
		if err := matchBundles(d, bundles); err != nil {
			return nil, err
		}
	}

	opts.workDir = tmp
	r, err := Run(descPath, opts)
	if err != nil {
		return nil, relError(tmp, root, err)
	}
	r.Descriptor = DescriptorName
	for i := range r.Findings {
		f := &r.Findings[i]
		f.File = relPath(tmp, root, f.File)
		f.Message = relText(tmp, root, f.Message)
	}
	for i := range r.APIs {
		for j := range r.APIs[i].Changes {
			c := &r.APIs[i].Changes[j]
			c.File = relPath(tmp, root, c.File)
			c.Message = relText(tmp, root, c.Message)
		}
	}
	return r, nil
}

// writeBundles writes every bundle's files under root. Bundles may share
// files, such as a common schema, but only with identical bytes.
func writeBundles(root string, bundles map[string]*bundle.Bundle) error {
	written := map[string]string{} // path → id of the bundle that wrote it
	for _, id := range slices.Sorted(maps.Keys(bundles)) {
		b := bundles[id]
		for p, data := range b.Files {
			if p == DescriptorName {
				return fmt.Errorf("bundle %s: contains %s, the descriptor's own path", id, p)
			}
			if other, ok := written[p]; ok {
				if !bytes.Equal(bundles[other].Files[p], data) {
					return fmt.Errorf("bundles %s and %s have different contents for %s", other, id, p)
				}
				continue
			}
			written[p] = id
		}
		if err := b.WriteDir(root); err != nil {
			return fmt.Errorf("bundle %s: %w", id, err)
		}
	}
	return nil
}

// matchBundles checks there is one bundle per API of a kind that is parsed,
// and that its entry is the spec the descriptor declares.
func matchBundles(d *descriptor.Descriptor, bundles map[string]*bundle.Bundle) error {
	want := map[string]string{} // id → declared spec, as a clean slash path
	for _, a := range d.APIs {
		want[a.ID] = path.Clean(filepath.ToSlash(a.Spec))
	}
	for _, id := range slices.Sorted(maps.Keys(bundles)) {
		spec, ok := want[id]
		if !ok {
			return fmt.Errorf("bundle %s: no API with that id in the descriptor", id)
		}
		if entry := bundles[id].Entry; entry != spec {
			return fmt.Errorf("bundle %s: entry is %s, but the descriptor's spec is %s", id, entry, spec)
		}
	}
	for _, a := range d.APIs {
		if _, ok := bundles[a.ID]; !ok && a.Kind != descriptor.KindAsyncAPI {
			return fmt.Errorf("no bundle for API %s", a.ID)
		}
	}
	return nil
}

// relPath makes p relative to root when it is inside it. Baseline files,
// unpacked elsewhere under tmp, become "baseline:<id>/<path>".
func relPath(tmp, root, p string) string {
	if p == "" {
		return p
	}
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	if rel, err := filepath.Rel(tmp, p); err == nil && !strings.HasPrefix(rel, "..") {
		// portal-baseline-*/<id>/<path>
		if _, rest, ok := strings.Cut(filepath.ToSlash(rel), "/"); ok {
			return "baseline:" + rest
		}
	}
	return p
}

// relText rewrites the temporary paths inside a message.
func relText(tmp, root, s string) string {
	s = strings.ReplaceAll(s, root+string(filepath.Separator), "")
	if !strings.Contains(s, tmp) {
		return s
	}
	var out strings.Builder
	for {
		i := strings.Index(s, tmp+string(filepath.Separator))
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i])
		s = s[i+len(tmp)+1:]
		// Skip the portal-baseline-* directory name.
		if j := strings.IndexByte(s, filepath.Separator); j >= 0 {
			out.WriteString("baseline:")
			s = s[j+1:]
		}
	}
}

func relError(tmp, root string, err error) error {
	msg := relText(tmp, root, err.Error())
	if msg == err.Error() {
		return err
	}
	return fmt.Errorf("%s", msg)
}

// BundleAPIs packs the spec closure of every API whose spec is usable, as a
// push sends them. Problems with a closure are findings (bundle-syntax,
// bundle-ref) and leave that API out.
func BundleAPIs(d *descriptor.Descriptor) (map[string]*bundle.Bundle, []model.Finding, error) {
	bundles := map[string]*bundle.Bundle{}
	var findings []model.Finding
	for i, api := range d.APIs {
		if !d.SpecOK(i) {
			continue // already a finding
		}
		c, problems, err := bundle.Load(d.Dir, d.SpecPath(i))
		var se *yamldoc.SyntaxError
		if errors.As(err, &se) {
			findings = append(findings, model.Finding{API: api.ID, RuleID: "bundle-syntax", Severity: model.SeverityError,
				File: d.SpecPath(i), Line: se.Line, Message: "not valid YAML or JSON: " + se.Error()})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		for _, p := range problems {
			findings = append(findings, model.Finding{API: api.ID, RuleID: "bundle-ref", Severity: model.SeverityError,
				File: p.File, Pointer: p.Pointer, Line: p.Line, Message: p.Message})
		}
		if len(problems) > 0 {
			continue
		}
		b, err := bundle.Read(c.Root, c.Files())
		if err != nil {
			return nil, nil, err
		}
		bundles[api.ID] = b
	}
	return bundles, findings, nil
}

// ParseBundle parses a stored bundle's spec into the model, without linting
// or diffing, to reindex a published version. Paths in the model are
// relative to the bundle's root, as they were at push time.
func ParseBundle(b *bundle.Bundle) (*model.Spec, error) {
	tmp, err := os.MkdirTemp("", "portal-parse-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := b.WriteDir(tmp); err != nil {
		return nil, err
	}
	entry := filepath.Join(tmp, filepath.FromSlash(b.Entry))
	kind, finding, err := descriptor.Sniff(entry)
	if err != nil {
		return nil, relError(tmp, tmp, err)
	}
	if finding != nil {
		return nil, fmt.Errorf("%s: %s", b.Entry, finding.Message)
	}
	var spec *model.Spec
	var findings []model.Finding
	switch kind {
	case descriptor.KindCloudEvents:
		res, fs, err := eventcatalog.Parse(tmp, entry)
		if err != nil {
			return nil, relError(tmp, tmp, err)
		}
		findings = fs
		if res != nil {
			spec = res.Spec
		}
	case descriptor.KindOpenAPI:
		res, fs, err := openapi.Parse(tmp, entry)
		if err != nil {
			return nil, relError(tmp, tmp, err)
		}
		findings = fs
		if res != nil {
			spec = res.Spec
		}
	default:
		return nil, fmt.Errorf("%s: %s specs aren't parsed yet", b.Entry, kind)
	}
	if spec == nil {
		msg := "can't be parsed"
		if len(findings) > 0 {
			msg = relText(tmp, tmp, findings[0].Message)
		}
		return nil, fmt.Errorf("%s: %s", b.Entry, msg)
	}
	return spec, nil
}
