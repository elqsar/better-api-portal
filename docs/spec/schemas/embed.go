// Package schemas embeds the JSON Schemas for the files teams write, so the
// spec directory stays the single source of truth for both docs and code.
package schemas

import "embed"

// FS holds portal.schema.json and eventcatalog.schema.json.
//
//go:embed *.json
var FS embed.FS
