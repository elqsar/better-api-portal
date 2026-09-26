package web

import (
	"bytes"
	"net/http"
	"strconv"
	"sync"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/spec/openapi"
)

// documentVersion is part of the document's ETag: bump it when
// openapi.Document's output changes, so browsers refetch.
const documentVersion = "1"

// docCache keeps the latest resolved documents by content hash. They are
// immutable, so there's nothing to invalidate.
type docCache struct {
	mu    sync.Mutex
	docs  map[string][]byte
	order []string
}

const docCacheSize = 32

func (c *docCache) get(hash string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.docs[hash]
}

func (c *docCache) put(hash string, doc []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.docs == nil {
		c.docs = map[string][]byte{}
	}
	if _, ok := c.docs[hash]; ok {
		return
	}
	if len(c.order) == docCacheSize {
		delete(c.docs, c.order[0])
		c.order = c.order[1:]
	}
	c.docs[hash] = doc
	c.order = append(c.order, hash)
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
	doc := s.docs.get(hash)
	if doc == nil {
		data, err := s.Store.Bundle(r.Context(), hash)
		if err != nil || data == nil {
			s.fail(w, r, u, err)
			return
		}
		b, err := bundle.Unpack(bytes.NewReader(data))
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
