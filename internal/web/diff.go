package web

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"golang.org/x/mod/semver"

	"github.com/elqsar/better-api-portal/internal/bundle"
	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/compat"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/store"
	"github.com/elqsar/better-api-portal/internal/textdiff"
	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// diffContext is how many unchanged lines show around a change.
const diffContext = 3

// versionDiff is everything the diff page shows for two content hashes, so
// it never changes and is cached.
type versionDiff struct {
	Changes    []model.Change
	ChangesErr string
	YAML, JSON []textdiff.File
	JSONErr    string
}

type diffData struct {
	API       *store.APIDetail
	OwnerName string
	// Published versions, newest first, for the pickers.
	Versions []string
	From, To *store.Version
	// Backwards: from is the later version.
	Backwards bool
	View      string // changes | raw
	Mode      string // yaml | json (raw view)
	Full      bool   // raw view without folding
	// Nothing is compared, and why.
	Empty string

	Diff   *versionDiff
	Files  []diffFile
	Counts map[string]int // changes by impact
}

type diffFile struct {
	textdiff.File
	Blocks []textdiff.Block
}

// impactOrder sorts changes most severe first.
var impactOrder = map[model.Impact]int{model.ImpactBreaking: 0, model.ImpactWarn: 1, model.ImpactAdditive: 2, model.ImpactDocs: 3}

// apiDiff compares two published versions: the contract changes, or the
// raw files. from defaults to the version before to, to to the latest.
func (s *Server) apiDiff(w http.ResponseWriter, r *http.Request, u *User) {
	d := s.loadDiff(w, r, u)
	if d == nil {
		return
	}
	if d.Diff != nil {
		d.Counts = map[string]int{}
		for _, c := range d.Diff.Changes {
			d.Counts[string(c.Impact)]++
		}
		files := d.Diff.YAML
		if d.Mode == "json" {
			files = d.Diff.JSON
		}
		for _, f := range files {
			df := diffFile{File: f}
			if d.Full {
				df.Blocks = []textdiff.Block{{Start: 0, End: len(f.Lines), Lines: f.Lines}}
			} else {
				df.Blocks = textdiff.Fold(f.Lines, diffContext)
			}
			d.Files = append(d.Files, df)
		}
	}
	p := page{Title: d.API.Name() + " diff", Nav: "apis", User: u, Data: d}
	if htmx(r) {
		w.Header().Set("HX-Push-Url", pushURL(r.URL.Path, r.URL.Query()))
		s.renderBlock(w, r, http.StatusOK, "diff", "diff-body", p)
		return
	}
	s.render(w, r, http.StatusOK, "diff", p)
}

// diffLines serves a folded run of unchanged lines, for htmx to expand.
func (s *Server) diffLines(w http.ResponseWriter, r *http.Request, u *User) {
	d := s.loadDiff(w, r, u)
	if d == nil {
		return
	}
	q := r.URL.Query()
	var lines []textdiff.Line
	if d.Diff != nil {
		files := d.Diff.YAML
		if d.Mode == "json" {
			files = d.Diff.JSON
		}
		start, err1 := strconv.Atoi(q.Get("start"))
		end, err2 := strconv.Atoi(q.Get("end"))
		i := slices.IndexFunc(files, func(f textdiff.File) bool { return f.Path == q.Get("file") })
		if err1 == nil && err2 == nil && i >= 0 && 0 <= start && start <= end && end <= len(files[i].Lines) {
			lines = files[i].Lines[start:end]
		}
	}
	if lines == nil {
		http.Error(w, "no such lines", http.StatusNotFound)
		return
	}
	// Both sides are content hashes, so the lines never change.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	s.renderBlock(w, r, http.StatusOK, "diff", "diff-lines", page{User: u, Data: textdiff.Block{Lines: lines}})
}

// loadDiff resolves the versions in the query and loads their diff. It
// writes the response itself and returns nil when there's no page.
func (s *Server) loadDiff(w http.ResponseWriter, r *http.Request, u *User) *diffData {
	a := s.loadAPI(w, r, u, "")
	if a == nil {
		return nil
	}
	q := r.URL.Query()
	d := &diffData{API: a.API, OwnerName: a.OwnerName,
		View: cmp.Or(q.Get("view"), "changes"), Mode: cmp.Or(q.Get("mode"), "yaml"), Full: q.Get("full") == "1"}
	if d.View != "raw" {
		d.View = "changes"
	}
	if d.Mode != "json" {
		d.Mode = "yaml"
	}
	versions, err := s.Store.Versions(r.Context(), a.API.ID)
	if err != nil {
		s.fail(w, r, u, err)
		return nil
	}
	d.Versions = published(versions)

	to := cmp.Or(q.Get("to"), a.API.LatestSemver)
	from := q.Get("from")
	if from == "" {
		// The version before to.
		if i := slices.Index(d.Versions, to); i >= 0 && i+1 < len(d.Versions) {
			from = d.Versions[i+1]
		}
	}
	switch {
	case len(d.Versions) < 2 && q.Get("from") == "":
		d.Empty = "Only one version is published, so there is nothing to compare yet."
		return d
	case from == "":
		d.Empty = to + " is the first published version; pick an earlier one to compare with."
		return d
	}
	for _, sv := range []struct {
		semver string
		v      **store.Version
	}{{from, &d.From}, {to, &d.To}} {
		v, err := s.Store.PublishedVersion(r.Context(), a.API.ID, sv.semver)
		if err != nil {
			s.fail(w, r, u, err)
			return nil
		}
		if v == nil {
			s.error(w, r, u, http.StatusNotFound, "No such version", a.API.ID+" has no published version "+sv.semver+".")
			return nil
		}
		*sv.v = v
	}
	d.Backwards = semver.Compare("v"+d.From.Semver, "v"+d.To.Semver) > 0
	if d.From.ContentHash == d.To.ContentHash {
		d.Empty = "Both versions have the same content."
		return d
	}
	if d.Diff, err = s.versionDiff(r, d.From.ContentHash, d.To.ContentHash, compat.Mode(a.API.Compatibility)); err != nil {
		s.fail(w, r, u, err)
		return nil
	}
	return d
}

func (s *Server) versionDiff(r *http.Request, from, to string, mode compat.Mode) (*versionDiff, error) {
	key := from + " " + to + " " + string(mode)
	if vd, ok := s.diffs.get(key); ok {
		return vd, nil
	}
	var bs [2]*bundle.Bundle
	for i, h := range []string{from, to} {
		b, err := s.unpack(r.Context(), h)
		if err != nil {
			return nil, err
		}
		bs[i] = b
	}
	vd := &versionDiff{}
	changes, err := check.DiffBundles(bs[0], bs[1], mode)
	if err != nil {
		vd.ChangesErr = err.Error()
	}
	slices.SortStableFunc(changes, func(a, b model.Change) int { return impactOrder[a.Impact] - impactOrder[b.Impact] })
	vd.Changes = changes

	var raw, canon [2]map[string]string
	for i, b := range bs {
		raw[i], canon[i] = map[string]string{}, map[string]string{}
		for p, data := range b.Files {
			raw[i][p] = string(data)
			c, err := canonical(p, data)
			if err != nil {
				vd.JSONErr = err.Error()
			}
			canon[i][p] = c
		}
	}
	vd.YAML = textdiff.Files(raw[0], raw[1])
	if vd.JSONErr == "" {
		vd.JSON = textdiff.Files(canon[0], canon[1])
	}
	s.diffs.put(key, vd)
	return vd, nil
}

// canonical is a file as the content hash sees it (sorted keys, no
// comments or formatting), indented for reading.
func canonical(path string, data []byte) (string, error) {
	doc, err := yamldoc.Parse(path, data)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc.JSON()); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// diffURL is the diff page's address with one parameter changed.
func diffURL(d *diffData, key, value string) string {
	v := url.Values{"view": {d.View}}
	if d.From != nil {
		v.Set("from", d.From.Semver)
		v.Set("to", d.To.Semver)
	}
	if d.View == "raw" {
		v.Set("mode", d.Mode)
	}
	if d.Full {
		v.Set("full", "1")
	}
	if value == "" {
		v.Del(key)
	} else {
		v.Set(key, value)
	}
	if v.Get("view") == "changes" {
		v.Del("view")
		v.Del("mode")
		v.Del("full")
	}
	if v.Get("mode") == "yaml" {
		v.Del("mode")
	}
	return "/apis/" + url.PathEscape(d.API.ID) + "/diff?" + v.Encode()
}

// published lists the published versions, highest first.
func published(vs []store.VersionSummary) []string {
	var out []string
	for _, v := range vs {
		if v.Status == store.StatusPublished {
			out = append(out, v.Semver)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return semver.Compare("v"+b, "v"+a) })
	return out
}

// foldURL fetches a folded block's lines.
func foldURL(d *diffData, file string, b textdiff.Block) string {
	v := url.Values{"from": {d.From.Semver}, "to": {d.To.Semver}, "file": {file},
		"start": {strconv.Itoa(b.Start)}, "end": {strconv.Itoa(b.End)}}
	if d.Mode == "json" {
		v.Set("mode", "json")
	}
	return "/apis/" + url.PathEscape(d.API.ID) + "/diff/lines?" + v.Encode()
}
