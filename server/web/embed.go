// Package web embeds the React admin UI static files into the Go binary.
package web

import "embed"

// Files contains the contents of the admin/ directory. It is populated at
// build time by copying the admin-ui dist output into server/web/admin.
//
//go:embed all:admin
var Files embed.FS
