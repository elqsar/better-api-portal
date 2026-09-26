package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/check"
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/model"
	"better-api-portal/internal/store"
)

// Push request limits. Bundles have their own per-file and total limits.
const (
	maxPushSize       = 64 << 20
	maxDescriptorSize = 1 << 20
)

// Multipart part names of a push.
const (
	PartDescriptor   = "descriptor"
	PartBundlePrefix = "bundle:" // followed by the API id
	PartAcks         = "acks"    // JSON object: change id → reason
)

// API statuses in a push response.
const (
	StatusPublished = "published" // stored as a new version
	StatusUnchanged = "unchanged" // this version is published already with the same content
	StatusAccepted  = "accepted"  // a dry run that push would publish
	StatusRejected  = "rejected"
	StatusSkipped   = "skipped" // a kind the portal doesn't process yet
)

// PushResponse is the body of a push or check: the shape of
// `portal check --format json`, with a status per API.
type PushResponse struct {
	Findings []model.Finding `json:"findings"`
	APIs     []PushResult    `json:"apis"`
}

// PushResult is one API's outcome.
type PushResult struct {
	check.APIResult
	Status string `json:"status"`
	URL    string `json:"url,omitempty"`
}

// pushRequest is a parsed push body.
type pushRequest struct {
	descriptor []byte
	bundles    map[string]*bundle.Bundle
	acks       map[string]string
}

// push handles POST /push, or /check with dryRun, which runs the same
// pipeline and stores nothing.
func (s *Server) push(dryRun bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if who := identity(r); !dryRun && !who.CanPush {
			writeError(w, http.StatusForbidden, fmt.Sprintf("%s may not push from ref %q; it can still check", who.Repo, who.Ref))
			return
		}
		req, err := readPush(w, r)
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a push is limited to %d bytes", tooBig.Limit))
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := s.process(r.Context(), identity(r), req, dryRun)
		var bad badRequest
		switch {
		case errors.As(err, &bad):
			writeError(w, http.StatusBadRequest, bad.Error())
		case err != nil:
			s.Log.Error("push failed", "repo", identity(r).Repo, "err", err)
			writeError(w, http.StatusInternalServerError, "the push could not be processed")
		default:
			writeJSON(w, http.StatusOK, resp)
		}
	}
}

// badRequest is an error the client caused.
type badRequest struct{ error }

func readPush(w http.ResponseWriter, r *http.Request) (*pushRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPushSize)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("want a multipart/form-data body: %w", err)
	}
	req := &pushRequest{bundles: map[string]*bundle.Bundle{}}
	seen := map[string]bool{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := part.FormName()
		if seen[name] {
			return nil, fmt.Errorf("part %q appears more than once", name)
		}
		seen[name] = true
		switch {
		case name == PartDescriptor:
			if req.descriptor, err = io.ReadAll(io.LimitReader(part, maxDescriptorSize+1)); err != nil {
				return nil, err
			}
			if len(req.descriptor) > maxDescriptorSize {
				return nil, fmt.Errorf("the descriptor is larger than %d bytes", maxDescriptorSize)
			}
		case strings.HasPrefix(name, PartBundlePrefix):
			id := strings.TrimPrefix(name, PartBundlePrefix)
			b, err := bundle.Unpack(part)
			if err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					return nil, err
				}
				return nil, fmt.Errorf("bundle %s: %w", id, err)
			}
			req.bundles[id] = b
		case name == PartAcks:
			if err := json.NewDecoder(io.LimitReader(part, maxDescriptorSize)).Decode(&req.acks); err != nil {
				return nil, fmt.Errorf("acks: want a JSON object of change id to reason: %w", err)
			}
			for id, reason := range req.acks {
				if strings.TrimSpace(reason) == "" {
					return nil, fmt.Errorf("ack %s has no reason: say why the breaking change is safe", id)
				}
			}
		default:
			return nil, fmt.Errorf("unexpected part %q (want %s, %s<api-id> or %s)", name, PartDescriptor, PartBundlePrefix, PartAcks)
		}
	}
	if req.descriptor == nil {
		return nil, fmt.Errorf("no %s part", PartDescriptor)
	}
	return req, nil
}

// process runs the pipeline over a push against the store's baselines and,
// unless dryRun, records each API's outcome. APIs are independent: one can
// be rejected while another publishes.
func (s *Server) process(ctx context.Context, who *Identity, req *pushRequest, dryRun bool) (*PushResponse, error) {
	// The descriptor's metadata. RunBundles validates it; if it doesn't
	// decode, the findings say why and there are no APIs to record.
	var d descriptor.Descriptor
	yaml.Unmarshal(req.descriptor, &d)

	claimedBy := map[string]string{} // API id → the other repo that owns it
	stored := map[string]*store.APIRecord{}
	baselines := map[string]check.Baseline{}
	for _, id := range slices.Sorted(maps.Keys(req.bundles)) {
		rec, err := s.Store.API(ctx, id)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.Repo != who.Repo {
			claimedBy[id] = rec.Repo
			continue // not this repo's history to compare against
		}
		stored[id] = rec
		base, err := s.baseline(ctx, rec)
		if err != nil {
			return nil, err
		}
		if base != nil {
			baselines[id] = *base
		}
	}

	r, err := check.RunBundles(req.descriptor, req.bundles, check.Options{Config: s.Config, BaselineBundles: baselines, Acks: req.acks})
	if err != nil {
		return nil, badRequest{err}
	}
	resp := &PushResponse{Findings: r.Findings, APIs: []PushResult{}}
	consumes, err := s.unknownConsumes(ctx, &d, req.bundles)
	if err != nil {
		return nil, err
	}
	resp.Findings = append(resp.Findings, consumes...)
	descErrors := hasError(resp.Findings, "")

	var repoID int64
	if !dryRun && len(r.APIs) > 0 {
		if repoID, err = s.Store.Repo(ctx, who.Repo); err != nil {
			return nil, err
		}
	}
	results := map[string]check.APIResult{}
	for _, a := range r.APIs {
		results[a.ID] = a
	}
	now := time.Now().UTC()
	for _, api := range d.APIs {
		res, parsed := results[api.ID]
		out := PushResult{APIResult: res, Status: StatusRejected}
		out.ID = api.ID
		if api.Kind == descriptor.KindAsyncAPI {
			out.Status = StatusSkipped
			resp.APIs = append(resp.APIs, out)
			continue
		}
		reject := func(rule, msg string) {
			resp.Findings = append(resp.Findings, model.Finding{API: api.ID, RuleID: rule,
				Severity: model.SeverityError, Message: msg, File: check.DescriptorName})
		}
		if other, ok := claimedBy[api.ID]; ok {
			reject("api-claimed", fmt.Sprintf("%s is claimed by %s; an admin can transfer it, or publish under a new id", api.ID, other))
			if !dryRun {
				s.auditClaim(ctx, who, api.ID, other)
			}
			resp.APIs = append(resp.APIs, out)
			continue
		}
		if !parsed {
			resp.APIs = append(resp.APIs, out) // the findings say why; there is no version to record
			continue
		}

		existing, err := s.Store.PublishedVersion(ctx, api.ID, res.Version)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.ContentHash == res.ContentHash {
				out.Status = StatusUnchanged // a CI retry: nothing to do
				out.URL = s.versionURL(api.ID, res.Version)
			} else {
				reject("version-immutable", fmt.Sprintf("%s %s is already published with different content; bump the version", api.ID, res.Version))
			}
			resp.APIs = append(resp.APIs, out)
			continue
		}

		ok := !descErrors && !hasError(resp.Findings, api.ID)
		if dryRun {
			if ok {
				out.Status = StatusAccepted
			}
			resp.APIs = append(resp.APIs, out)
			continue
		}
		status := store.StatusRejected
		if ok {
			status = store.StatusPublished
		}
		if !ok && stored[api.ID] == nil {
			// Only a successful push claims an id, so a rejected first
			// push has nothing to be recorded against.
			resp.APIs = append(resp.APIs, out)
			continue
		}
		packed, err := pack(req.bundles[api.ID])
		if err != nil {
			return nil, err
		}
		_, err = s.Store.Record(ctx, store.Push{
			API:    apiMeta(&d, api),
			RepoID: repoID,
			Actor:  who.Actor,
			Version: store.Version{
				Semver:      res.Version,
				Prerelease:  semver.Prerelease("v"+res.Version) != "",
				ContentHash: res.ContentHash,
				Status:      status,
				Source: store.Source{Repo: who.Repo, Commit: who.Commit, Ref: who.Ref,
					CIRunURL: who.RunURL, PushedBy: who.Actor, PushedAt: now},
			},
			Bundle:          packed,
			Acks:            usedAcks(req.acks, res.Changes),
			Score:           res.Score,
			Findings:        forAPI(resp.Findings, api.ID),
			BaselineVersion: res.BaselineVersion,
			Changes:         res.Changes,
		})
		switch {
		case errors.Is(err, store.ErrClaimed):
			// Another repo claimed the id since we looked.
			reject("api-claimed", fmt.Sprintf("%s was claimed by another repo during this push", api.ID))
			status = store.StatusRejected
		case errors.Is(err, store.ErrVersionExists):
			// A concurrent push of the same version won; it was either this
			// content (a retry) or not.
			v, err := s.Store.PublishedVersion(ctx, api.ID, res.Version)
			if err != nil {
				return nil, err
			}
			if v != nil && v.ContentHash == res.ContentHash {
				status = StatusUnchanged
			} else {
				reject("version-immutable", fmt.Sprintf("%s %s was published with different content during this push", api.ID, res.Version))
				status = store.StatusRejected
			}
		case err != nil:
			return nil, err
		}
		out.Status = status
		if status != store.StatusRejected {
			out.URL = s.versionURL(api.ID, res.Version)
		}
		resp.APIs = append(resp.APIs, out)
		s.Log.Info("push", "api", api.ID, "version", res.Version, "status", out.Status, "repo", who.Repo)
	}
	return resp, nil
}

// baseline is the latest published, non-pre-release version of a stored API,
// or nil.
func (s *Server) baseline(ctx context.Context, rec *store.APIRecord) (*check.Baseline, error) {
	if rec == nil {
		return nil, nil
	}
	v, err := s.Store.LatestPublished(ctx, rec.ID)
	if err != nil || v == nil {
		return nil, err
	}
	data, err := s.Store.Bundle(ctx, v.ContentHash)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("bundle %s of %s %s is missing", v.ContentHash, rec.ID, v.Semver)
	}
	b, err := bundle.Unpack(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("stored bundle of %s %s: %w", rec.ID, v.Semver, err)
	}
	return &check.Baseline{Bundle: b, Lifecycle: rec.Lifecycle}, nil
}

// unknownConsumes warns about consumed API ids that are neither in the
// portal nor in this push; repos may onboard in any order.
func (s *Server) unknownConsumes(ctx context.Context, d *descriptor.Descriptor, pushed map[string]*bundle.Bundle) ([]model.Finding, error) {
	var fs []model.Finding
	for i, c := range d.Consumes {
		if _, ok := pushed[c.API]; ok || c.API == "" {
			continue
		}
		rec, err := s.Store.API(ctx, c.API)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			fs = append(fs, model.Finding{RuleID: "consumes-unknown-api", Severity: model.SeverityWarn,
				Message: fmt.Sprintf("consumes %s, which isn't in the portal yet", c.API),
				File:    check.DescriptorName, Pointer: fmt.Sprintf("/consumes/%d/api", i)})
		}
	}
	return fs, nil
}

func (s *Server) auditClaim(ctx context.Context, who *Identity, id, owner string) {
	if err := s.Store.Audit(ctx, who.Actor, "push.claim-rejected", id,
		map[string]string{"repo": who.Repo, "owner": owner}); err != nil {
		s.Log.Error("audit", "err", err)
	}
}

func (s *Server) versionURL(id, version string) string {
	return s.url("/apis/" + id + "/versions/" + version)
}

// apiMeta is the descriptor-level metadata the push stores on the API.
func apiMeta(d *descriptor.Descriptor, api descriptor.API) store.API {
	owner := api.Owner
	if owner == "" {
		owner = d.Owner
	}
	meta := map[string]any{}
	set := func(k string, v any, empty bool) {
		if !empty {
			meta[k] = v
		}
	}
	set("title", api.Title, api.Title == "")
	set("system", d.System, d.System == "")
	set("tags", api.Tags, len(api.Tags) == 0)
	links := append(slices.Clone(d.Links), api.Links...)
	set("links", links, len(links) == 0)
	set("environments", api.Environments, len(api.Environments) == 0)
	set("compatibility", api.Compatibility, api.Compatibility == "")
	set("consumes", d.Consumes, len(d.Consumes) == 0)
	return store.API{ID: api.ID, Kind: string(api.Kind), Owner: owner,
		Lifecycle: api.Lifecycle, Sunset: api.Sunset, Meta: meta}
}

// usedAcks are the acks that match one of the API's changes.
func usedAcks(acks map[string]string, changes []model.Change) map[string]string {
	used := map[string]string{}
	for _, c := range changes {
		if reason, ok := acks[c.ID]; ok && c.ID != "" {
			used[c.ID] = reason
		}
	}
	return used
}

func forAPI(fs []model.Finding, id string) []model.Finding {
	var out []model.Finding
	for _, f := range fs {
		if f.API == id || f.API == "" {
			out = append(out, f)
		}
	}
	return out
}

func hasError(fs []model.Finding, api string) bool {
	return slices.ContainsFunc(fs, func(f model.Finding) bool {
		return f.API == api && f.Severity == model.SeverityError
	})
}

func pack(b *bundle.Bundle) ([]byte, error) {
	var buf bytes.Buffer
	if err := b.Pack(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
