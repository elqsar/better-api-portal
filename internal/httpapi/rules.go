package httpapi

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/descriptor"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/store"
)

// pushedAPI is an API of this push that the portal-wide rules look at.
type pushedAPI struct {
	id, owner string
	spec      *model.Spec
}

// portalRules are the lint rules that need the rest of the portal
// (04-governance): ce-type-unique and ce-topic-single-owner. Each pushed
// API is compared with the latest versions in the store and with the other
// APIs in this push. APIs claimed by another repo are left out: they are
// rejected anyway.
func (s *Server) portalRules(ctx context.Context, d *descriptor.Descriptor, results []check.APIResult, claimedBy map[string]string) ([]model.Finding, error) {
	specs := map[string]*model.Spec{}
	for _, r := range results {
		specs[r.ID] = r.Spec
	}
	var pushed []pushedAPI
	for _, api := range d.APIs {
		spec := specs[api.ID]
		if _, claimed := claimedBy[api.ID]; claimed || spec == nil || spec.Kind != string(descriptor.KindCloudEvents) {
			continue
		}
		pushed = append(pushed, pushedAPI{id: api.ID, owner: apiMeta(d, api).Owner, spec: spec})
	}
	if len(pushed) == 0 {
		return nil, nil
	}
	types, err := s.typeUnique(ctx, pushed)
	if err != nil {
		return nil, err
	}
	topics, err := s.topicSingleOwner(ctx, pushed)
	if err != nil {
		return nil, err
	}
	return append(types, topics...), nil
}

// typeUnique: a type is declared by at most one API (02-domain-model,
// invariant 1). It is strict: an API in this push that drops a type
// another one adds still holds it until its new version is published, so
// moving a type between APIs takes two pushes. Retired APIs keep their
// types.
func (s *Server) typeUnique(ctx context.Context, pushed []pushedAPI) ([]model.Finding, error) {
	var types []string
	for _, p := range pushed {
		for _, m := range p.spec.Messages {
			types = append(types, m.Key)
		}
	}
	stored, err := s.Store.TypeDeclarations(ctx, types)
	if err != nil {
		return nil, err
	}
	var fs []model.Finding
	for _, p := range pushed {
		for _, m := range p.spec.Messages {
			var others []string
			for _, d := range stored {
				if d.Type != m.Key || d.APIID == p.id {
					continue
				}
				desc := fmt.Sprintf("%s (%s, %s)", d.APIID, d.Role, d.Semver)
				if d.Lifecycle == "retired" {
					desc = fmt.Sprintf("%s (%s, %s, retired: retired APIs keep their types)", d.APIID, d.Role, d.Semver)
				}
				others = append(others, desc)
			}
			for _, o := range pushed {
				if o.id != p.id && !slices.ContainsFunc(stored, func(d store.TypeDeclaration) bool { return d.APIID == o.id && d.Type == m.Key }) &&
					slices.ContainsFunc(o.spec.Messages, func(x model.Message) bool { return x.Key == m.Key }) {
					others = append(others, o.id+" (in this push)")
				}
			}
			for _, other := range others {
				fs = append(fs, model.Finding{API: p.id, RuleID: "ce-type-unique", Severity: model.SeverityError,
					Message: fmt.Sprintf("%s is already declared by %s; a type has one owning API. "+
						"If this API only consumes it, list it under consumes in %s instead", m.Key, other, check.DescriptorName),
					File: entry(p.spec), Pointer: m.Pointer, Line: m.Line})
			}
		}
	}
	return fs, nil
}

// topicSingleOwner warns when a topic this API produces to is also produced
// to by an API of another owner team. One finding per topic, at the first
// message bound to it.
func (s *Server) topicSingleOwner(ctx context.Context, pushed []pushedAPI) ([]model.Finding, error) {
	type use struct {
		msg   model.Message
		topic store.Topic
	}
	uses := map[string][]use{} // by pushed API
	all := map[store.Topic]bool{}
	for _, p := range pushed {
		seen := map[store.Topic]bool{}
		for _, m := range p.spec.Messages {
			if m.Role != model.RoleProduces {
				continue
			}
			for _, b := range m.Bindings {
				t := store.Topic{Protocol: b.Protocol, Address: b.Address}
				if !seen[t] {
					seen[t], all[t] = true, true
					uses[p.id] = append(uses[p.id], use{m, t})
				}
			}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	stored, err := s.Store.TopicProducers(ctx, slices.Collect(maps.Keys(all)))
	if err != nil {
		return nil, err
	}
	inPush := map[string]bool{}
	for _, p := range pushed {
		inPush[p.id] = true
	}
	var fs []model.Finding
	for _, p := range pushed {
		for _, u := range uses[p.id] {
			var others []string
			// This push's APIs replace their stored versions.
			for _, sp := range stored {
				if sp.Topic == u.topic && !inPush[sp.APIID] && sp.Owner != p.owner {
					others = append(others, fmt.Sprintf("%s (%s)", sp.APIID, sp.Owner))
				}
			}
			for _, o := range pushed {
				if o.owner != p.owner && slices.ContainsFunc(uses[o.id], func(x use) bool { return x.topic == u.topic }) {
					others = append(others, fmt.Sprintf("%s (%s, in this push)", o.id, o.owner))
				}
			}
			if len(others) == 0 {
				continue
			}
			fs = append(fs, model.Finding{API: p.id, RuleID: "ce-topic-single-owner", Severity: model.SeverityWarn,
				Message: fmt.Sprintf("%s %s is also produced to by %s; a topic with one owner team keeps "+
					"retention, partitioning and ordering decisions in one place", u.topic.Protocol, u.topic.Address, joinAnd(others)),
				File: entry(p.spec), Pointer: u.msg.Pointer, Line: u.msg.Line})
		}
	}
	return fs, nil
}

// entry is the spec's entry file, relative to the descriptor.
func entry(spec *model.Spec) string {
	if len(spec.Files) == 0 {
		return ""
	}
	return spec.Files[0]
}

func joinAnd(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
