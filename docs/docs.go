// Package docs embeds the operator documentation so the supervisor can
// read it at runtime without a checkout.
package docs

import "embed"

// FS holds every markdown file in this directory.
//
//go:embed *.md
var FS embed.FS
