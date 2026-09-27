package httpapi

import (
	"net/http"
	"strconv"

	"github.com/elqsar/better-api-portal/internal/store"
)

// Headers on a bundle download, so a check can use it as its baseline.
const (
	HeaderVersion     = "X-Portal-Version"
	HeaderContentHash = "X-Portal-Content-Hash"
	HeaderLifecycle   = "X-Portal-Lifecycle"
)

// bundle serves GET /apis/{id}/versions/{version}/bundle: the packed
// tar.zst of a published version, or of the latest one for "latest".
func (s *Server) bundle(w http.ResponseWriter, r *http.Request) {
	id, version := r.PathValue("id"), r.PathValue("version")
	ctx := r.Context()
	api, err := s.Store.API(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if api == nil {
		writeError(w, http.StatusNotFound, "no API "+id)
		return
	}
	var v *store.Version
	if version == "latest" {
		v, err = s.Store.LatestPublished(ctx, id)
	} else {
		v, err = s.Store.PublishedVersion(ctx, id, version)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "no published version "+version+" of "+id)
		return
	}
	data, err := s.Store.Bundle(ctx, v.ContentHash)
	if err != nil || data == nil {
		s.fail(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zstd")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Content-Disposition", `attachment; filename="`+id+`-`+v.Semver+`.tar.zst"`)
	h.Set(HeaderVersion, v.Semver)
	h.Set(HeaderContentHash, v.ContentHash)
	h.Set(HeaderLifecycle, api.Lifecycle)
	w.Write(data)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
