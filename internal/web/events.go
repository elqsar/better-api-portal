package web

import (
	"context"
	"encoding/json"
	"net/http"

	"better-api-portal/internal/check"
	"better-api-portal/internal/model"
	"better-api-portal/internal/store"
)

// eventData is the event page (J4): one CloudEvents type across APIs.
type eventData struct {
	Type string
	// Declared lists the APIs whose latest version declares the type. There
	// should be one, the contract's owner (ce-type-unique); the page shows
	// the first.
	Declared []store.MessageRole
	Owner    *store.MessageRole
	Message  *model.Message
	// Consumers declare the type, or its whole API, in `consumes`.
	Consumers []store.Dependency

	Schema    *schemaNode
	SchemaErr string
	Examples  []exampleView
	Bindings  []bindingView
}

type exampleView struct{ Name, JSON string }

type prop struct{ Name, Value string }

type bindingView struct {
	model.Binding
	Props   []prop // sorted by name
	Brokers []brokerLink
}

// brokerLink is an environment of the owning API on a configured broker.
type brokerLink struct{ Env, Broker, UI string }

func (s *Server) event(w http.ResponseWriter, r *http.Request, u *User) {
	ctx := r.Context()
	d := &eventData{Type: r.PathValue("type")}
	var err error
	if d.Declared, err = s.Store.MessageRoles(ctx, d.Type); err != nil {
		s.fail(w, r, u, err)
		return
	}
	var owners []string
	for _, m := range d.Declared {
		owners = append(owners, m.APIID)
	}
	if d.Consumers, err = s.Store.TypeConsumers(ctx, d.Type, owners); err != nil {
		s.fail(w, r, u, err)
		return
	}
	if len(d.Declared) == 0 && len(d.Consumers) == 0 {
		s.error(w, r, u, http.StatusNotFound, "No such event type", "No API in the portal declares or consumes "+d.Type+".")
		return
	}
	if len(d.Declared) > 0 {
		d.Owner = &d.Declared[0]
		if err := s.loadMessage(ctx, d); err != nil {
			s.fail(w, r, u, err)
			return
		}
	}
	s.render(w, r, http.StatusOK, "event", page{Title: d.Type, Nav: "apis", User: u, Data: d})
}

// loadMessage fills in the owner's declaration of the type.
func (s *Server) loadMessage(ctx context.Context, d *eventData) error {
	spec, err := s.Store.Model(ctx, d.Owner.VersionID)
	if err != nil {
		return err
	}
	for i := range spec.Messages {
		if spec.Messages[i].Key == d.Type {
			d.Message = &spec.Messages[i]
		}
	}
	if d.Message == nil {
		return nil // the index and the model disagree; reindex fixes it
	}
	for _, e := range d.Message.Examples {
		b, err := json.MarshalIndent(e.Data, "", "  ")
		if err != nil {
			return err
		}
		d.Examples = append(d.Examples, exampleView{e.Name, string(b)})
	}

	api, err := s.Store.APIDetail(ctx, d.Owner.APIID)
	if err != nil {
		return err
	}
	for _, b := range d.Message.Bindings {
		v := bindingView{Binding: b}
		for _, k := range sortedKeys(b.Props) {
			v.Props = append(v.Props, prop{k, propValue(b.Props[k])})
		}
		if api != nil {
			for _, env := range api.Environments {
				br := s.Config.Broker(env.Broker)
				if br != nil && (br.Protocol == "" || br.Protocol == b.Protocol) {
					v.Brokers = append(v.Brokers, brokerLink{env.Name, br.Name, br.UI})
				}
			}
		}
		d.Bindings = append(d.Bindings, v)
	}

	// The schema documents aren't in the stored model, so the tree comes
	// from the bundle. A failure there leaves the rest of the page.
	if d.Message.Payload == "" {
		return nil
	}
	full, err := s.parsedSpec(ctx, d.Owner.ContentHash)
	if err == nil {
		d.Schema, err = schemaTree(full, d.Message.Payload)
	}
	if err != nil {
		s.Log.Error("payload schema", "type", d.Type, "api", d.Owner.APIID, "version", d.Owner.Semver, "err", err)
		d.SchemaErr = err.Error()
	}
	return nil
}

// parsedSpec parses a stored bundle, schemas included.
func (s *Server) parsedSpec(ctx context.Context, hash string) (*model.Spec, error) {
	if spec, ok := s.specs.get(hash); ok {
		return spec, nil
	}
	b, err := s.unpack(ctx, hash)
	if err != nil {
		return nil, err
	}
	spec, err := check.ParseBundle(b)
	if err != nil {
		return nil, err
	}
	s.specs.put(hash, spec)
	return spec, nil
}

// propValue shows a binding property: strings as they are, the rest as
// JSON.
func propValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
