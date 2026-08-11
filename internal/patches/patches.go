// Package patches holds the JavaScript that runs before any page script.
//
// Every .js file in this directory is embedded into the binary and evaluated
// on new document, in filename order. This is the part of Postern most likely
// to need changes as detection evolves — it is deliberately kept as plain
// JavaScript so it can be edited without touching Go.
//
// A patch that is wrong is worse than no patch: an inconsistent override is a
// stronger signal than the thing it tries to hide. Keep them minimal and only
// fix what a real Chrome under automation actually gets wrong.
package patches

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed *.js
var files embed.FS

// Script is one embedded patch, ready to be injected.
type Script struct {
	Name   string
	Source string
}

// All returns every embedded patch, ordered by filename.
func All() ([]Script, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	scripts := make([]Script, 0, len(names))
	for _, name := range names {
		src, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}
		scripts = append(scripts, Script{Name: name, Source: string(src)})
	}
	return scripts, nil
}
