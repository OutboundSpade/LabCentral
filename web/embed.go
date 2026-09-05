// Package webassets embeds the statically exported SvelteKit application.
// Production requires no JavaScript runtime.
package webassets

import (
	"embed"
	"io/fs"
)

// Include SvelteKit's underscore-prefixed _app directory.
//
//go:embed all:build
var content embed.FS

func FS() fs.FS {
	result, err := fs.Sub(content, "build")
	if err != nil {
		panic(err)
	}
	return result
}
