package descriptor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"better-api-portal/internal/model"
	"better-api-portal/internal/yamldoc"
)

// Load reads and validates the descriptor at path. Problems with the
// descriptor or the specs it points to are returned as findings; the error is
// reserved for failures to run the check at all, such as an unreadable file.
// The descriptor is nil when it doesn't match the schema.
func Load(path string) (*Descriptor, []model.Finding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	doc, err := yamldoc.Parse(path, b)
	var se *yamldoc.SyntaxError
	if errors.As(err, &se) {
		return nil, []model.Finding{{
			RuleID:   "descriptor-syntax",
			Severity: model.SeverityError,
			Message:  "not valid YAML: " + se.Error(),
			File:     path,
			Line:     se.Line,
		}}, nil
	}
	if err != nil {
		return nil, nil, err
	}

	sch, err := compiled()
	if err != nil {
		return nil, nil, err
	}
	violations, err := yamldoc.Validate(sch, doc.JSON(), describe)
	if err != nil {
		return nil, nil, err
	}
	if len(violations) > 0 {
		findings := make([]model.Finding, len(violations))
		for i, v := range violations {
			findings[i] = model.Finding{
				RuleID:   "descriptor-schema",
				Severity: model.SeverityError,
				Message:  v.Message,
				File:     path,
				Pointer:  v.Pointer,
				Line:     doc.Line(v.Pointer),
			}
		}
		return nil, findings, nil
	}

	d := &Descriptor{Path: path, Dir: filepath.Dir(path)}
	if err := doc.Decode(d); err != nil {
		return nil, nil, err
	}
	findings, err := d.check(doc)
	if err != nil {
		return nil, nil, err
	}
	return d, findings, nil
}

// check runs the rules the JSON Schema can't express.
func (d *Descriptor) check(doc *yamldoc.Doc) ([]model.Finding, error) {
	var findings []model.Finding
	d.specOK = map[int]bool{}
	add := func(rule, ptr, msg string) {
		findings = append(findings, model.Finding{
			RuleID:   rule,
			Severity: model.SeverityError,
			Message:  msg,
			File:     d.Path,
			Pointer:  ptr,
			Line:     doc.Line(ptr),
		})
	}

	seen := map[string]int{}
	for i, api := range d.APIs {
		base := "/apis/" + strconv.Itoa(i)
		if first, dup := seen[api.ID]; dup {
			add("descriptor-duplicate-id", base+"/id",
				fmt.Sprintf("api id %q is already used by apis[%d]", api.ID, first))
		} else {
			seen[api.ID] = i
		}

		specPath, msg := d.resolveSpec(api.Spec)
		if msg != "" {
			add("descriptor-spec-path", base+"/spec", msg)
			continue
		}
		kind, f, err := sniff(specPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			add("descriptor-spec-path", base+"/spec", fmt.Sprintf("spec file %s does not exist", api.Spec))
			continue
		case err != nil:
			return nil, err
		case f != nil:
			findings = append(findings, *f)
			continue
		}
		switch {
		case kind == "":
			add("descriptor-kind-mismatch", base+"/kind",
				fmt.Sprintf("kind is %s, but %s has no openapi, asyncapi or eventcatalog version key", api.Kind, api.Spec))
		case kind != api.Kind:
			add("descriptor-kind-mismatch", base+"/kind",
				fmt.Sprintf("kind is %s, but %s is %s", api.Kind, api.Spec, kind))
		default:
			d.specOK[i] = true
		}
	}
	return findings, nil
}

// resolveSpec returns the path of a spec file, or a message explaining why the
// descriptor's spec value is not acceptable.
func (d *Descriptor) resolveSpec(spec string) (string, string) {
	if filepath.IsAbs(spec) || filepath.VolumeName(spec) != "" {
		return "", fmt.Sprintf("spec %s must be a path relative to the descriptor", spec)
	}
	clean := filepath.Clean(filepath.FromSlash(spec))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Sprintf("spec %s escapes the descriptor's directory", spec)
	}
	p := filepath.Join(d.Dir, clean)
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return "", fmt.Sprintf("spec %s is a directory, not a file", spec)
	}
	return p, ""
}
