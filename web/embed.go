// Package web holds the templates and static files that are compiled into the binary.
package web

import "embed"

// FS contains templates/ and static/.
//
//go:embed templates static
var FS embed.FS
