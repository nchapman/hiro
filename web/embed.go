// Package web embeds the built React UI for serving from the Go binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed ui/dist/*
var distFS embed.FS

// DistFS returns the embedded web UI filesystem, rooted at the dist directory.
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "ui/dist")
}
