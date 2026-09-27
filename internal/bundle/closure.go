// Package bundle works out which files make up a spec: the entry file plus
// everything reachable through $ref, all inside the descriptor's directory.
package bundle

import (
	"errors"
	"fmt"
	gourl "net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// RelTo returns abs as a slash path relative to root, or an error if it
// lies outside it.
func RelTo(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", abs, root)
	}
	return filepath.ToSlash(rel), nil
}

// Problem is a $ref that can't be followed.
type Problem struct {
	File    string // the file holding the $ref
	Pointer string // where the $ref is
	Line    int
	Message string
}

// Closure is the set of files of a spec.
type Closure struct {
	Root  string                  // absolute
	Entry string                  // relative to Root
	Docs  map[string]*yamldoc.Doc // by path relative to Root
	paths map[string]string       // relative → path as read, for findings
}

// Files returns the entry file first, then the others sorted.
func (c *Closure) Files() []string {
	var rest []string
	for f := range c.Docs {
		if f != c.Entry {
			rest = append(rest, f)
		}
	}
	sort.Strings(rest)
	return append([]string{c.Entry}, rest...)
}

// Path returns the path a file was read from, as given to Load.
func (c *Closure) Path(rel string) string { return c.paths[rel] }

// Load parses entry and follows every $ref to another file, recursively.
// Refs to remote URLs, to files outside root, and to files that are missing
// or malformed are problems; the error is reserved for failures to read the
// entry file or resolve paths. A syntax error in the entry file is returned
// as a *yamldoc.SyntaxError.
func Load(root, entry string) (*Closure, []Problem, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	absEntry, err := filepath.Abs(entry)
	if err != nil {
		return nil, nil, err
	}
	rel, err := RelTo(absRoot, absEntry)
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(entry)
	if err != nil {
		return nil, nil, err
	}
	doc, err := yamldoc.Parse(entry, b)
	if err != nil {
		return nil, nil, err
	}
	c := &Closure{Root: absRoot, Entry: rel, Docs: map[string]*yamldoc.Doc{rel: doc}, paths: map[string]string{rel: entry}}
	var problems []Problem
	queue := []string{rel}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		d := c.Docs[cur]
		refs(d.JSON(), "", func(ptr, ref string) {
			problem := func(format string, args ...any) {
				problems = append(problems, Problem{File: c.paths[cur], Pointer: ptr, Line: d.Line(ptr), Message: fmt.Sprintf(format, args...)})
			}
			file, _, _ := strings.Cut(ref, "#")
			if file == "" {
				return
			}
			if u, err := gourl.Parse(file); err != nil || u.Scheme != "" || u.Host != "" {
				problem("remote $ref %s is not supported; reference a file in the repo", ref)
				return
			}
			target := path.Join(path.Dir(cur), file)
			if target == ".." || strings.HasPrefix(target, "../") {
				problem("$ref %s leaves the descriptor's directory", ref)
				return
			}
			if _, seen := c.Docs[target]; seen {
				return
			}
			p := filepath.Join(filepath.Dir(c.paths[cur]), filepath.FromSlash(file))
			b, err := os.ReadFile(p)
			if errors.Is(err, os.ErrNotExist) {
				problem("$ref %s: %s does not exist", ref, target)
				return
			}
			if err != nil {
				problem("$ref %s: %v", ref, err)
				return
			}
			td, err := yamldoc.Parse(p, b)
			if err != nil {
				problem("$ref %s: %s is not valid YAML or JSON: %v", ref, target, err)
				return
			}
			c.Docs[target], c.paths[target] = td, p
			queue = append(queue, target)
		})
	}
	return c, problems, nil
}

// refs calls fn for every "$ref" string in v, with the pointer of the object
// holding it, in a deterministic order.
func refs(v any, ptr string, fn func(ptr, ref string)) {
	switch v := v.(type) {
	case map[string]any:
		if r, ok := v["$ref"].(string); ok {
			fn(ptr, r)
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			refs(v[k], ptr+yamldoc.Pointer(k), fn)
		}
	case []any:
		for i, c := range v {
			refs(c, fmt.Sprintf("%s/%d", ptr, i), fn)
		}
	}
}
