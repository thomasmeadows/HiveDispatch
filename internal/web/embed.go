package web

import "embed"

// dist is the Vite build of web/ (committed, so `go build` needs no Node).
//
//go:embed all:dist
var dist embed.FS
