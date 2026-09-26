package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/spec/openapi"
)

// documentVersion is part of the document's ETag: bump it when
// openapi.Document's output changes, so browsers refetch.
const documentVersion = "1"

// cache keeps the latest values derived from bundles, by content hash.
// Bundles are immutable, so there's nothing to invalidate.
type cache[V any] struct {
	mu    sync.Mutex
	items map[string]V
	order []string
}

const cacheSize = 32

func (c *cache[V]) get(hash string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[hash]
	return v, ok
}

func (c *cache[V]) put(hash string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]V{}
	}
	if _, ok := c.items[hash]; ok {
		return
	}
	if len(c.order) == cacheSize {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
	c.items[hash] = v
	c.order = append(c.order, hash)
}

// unpack loads a stored bundle.
func (s *Server) unpack(ctx context.Context, hash string) (*bundle.Bundle, error) {
	data, err := s.Store.Bundle(ctx, hash)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("bundle %s is missing", hash)
	}
	return bundle.Unpack(bytes.NewReader(data))
}

// apiDocs is the Docs tab: Scalar renders the version's OpenAPI document.
// It is a full page load (Scalar's scripts don't run in an htmx swap), and
// its CSP admits Scalar's injected stylesheet by nonce.
func (s *Server) apiDocs(w http.ResponseWriter, r *http.Request, u *User) {
	d := s.loadAPI(w, r, u, r.PathValue("version"))
	if d == nil {
		return
	}
	if d.API.Kind != "openapi" {
		s.error(w, r, u, http.StatusNotFound, "No reference docs", d.API.ID+" isn't an OpenAPI API; its overview lists what it offers.")
		return
	}
	d.Tab = "docs"
	nonce := randomString()
	w.Header().Set("Content-Security-Policy", csp("'nonce-"+nonce+"'"))
	s.render(w, r, http.StatusOK, "api", page{Title: d.API.ID + " docs", Nav: "apis", User: u, Data: d, Wide: true, StyleNonce: nonce})
}

// openAPIDocument serves a version's bundle resolved into one document.
func (s *Server) openAPIDocument(w http.ResponseWriter, r *http.Request, u *User) {
	d := s.loadAPI(w, r, u, r.PathValue("version"))
	if d == nil {
		return
	}
	if d.API.Kind != "openapi" {
		http.NotFound(w, r)
		return
	}
	hash := d.Version.ContentHash
	etag := `"` + hash + "-" + documentVersion + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, max-age=3600")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	doc, ok := s.docs.get(hash)
	if !ok {
		b, err := s.unpack(r.Context(), hash)
		if err != nil {
			s.fail(w, r, u, err)
			return
		}
		if doc, err = openapi.Document(b); err != nil {
			s.Log.Error("openapi document", "api", d.API.ID, "version", d.Version.Semver, "err", err)
			http.Error(w, "the document can't be resolved: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		s.docs.put(hash, doc)
	}
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(doc)))
	w.Write(doc)
}
