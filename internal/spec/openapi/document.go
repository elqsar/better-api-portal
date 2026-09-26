package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"better-api-portal/internal/bundle"
)

// root is where a bundle's files appear to the loader; nothing is read
// from disk.
const root = "/bundle/"

// Document resolves a bundle into one self-contained OpenAPI document, as
// JSON, for renderers that take a single document (the UI's Scalar docs).
// $refs to other files in the bundle are moved into components; refs
// within the document are kept. Only the bundle's own files are read.
func Document(b *bundle.Bundle) ([]byte, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, u *url.URL) ([]byte, error) {
		if u.Scheme != "" && u.Scheme != "file" {
			return nil, fmt.Errorf("%s: remote $refs are not followed", u)
		}
		p := path.Clean(u.Path)
		data, ok := b.Files[strings.TrimPrefix(p, root)]
		if !strings.HasPrefix(p, root) || !ok {
			return nil, fmt.Errorf("%s: not in the bundle", u.Path)
		}
		return data, nil
	}
	entry, ok := b.Files[b.Entry]
	if !ok {
		return nil, fmt.Errorf("bundle has no entry file %s", b.Entry)
	}
	doc, err := loader.LoadFromDataWithPath(entry, &url.URL{Path: root + b.Entry})
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", b.Entry, err)
	}
	doc.InternalizeRefs(context.Background(), nil)
	return json.Marshal(doc)
}
