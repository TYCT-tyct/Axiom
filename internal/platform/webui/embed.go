package webui

import (
	"embed"
	"io/fs"
)

// built holds what `just web-embed` copied here. Without a build only NOT_BUILT.txt is in it, so the program still
// compiles and Embedded says what to run, instead of go:embed failing on a missing directory.
//
//go:embed all:build
var built embed.FS

// Embedded is the frontend this program was built with.
func Embedded() (*Frontend, error) {
	root, err := fs.Sub(built, "build")
	if err != nil {
		return nil, err
	}
	return New(root)
}
