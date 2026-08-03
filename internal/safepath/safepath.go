// Package safepath resolves the paths a client supplies against the reviewed
// directory. A review serves a whole source tree and has no access control of
// its own, so a path that arrives from outside has to be kept from reaching out
// of that tree.
package safepath

import (
	"path/filepath"
	"strings"
)

// Resolve joins relPath onto root and reports whether the result stays inside
// root. An empty path resolves to nothing.
//
// Unlike a check for ".." in the text, this accepts a filename that merely
// contains ".." (such as "[...slug].astro") while still rejecting a path that
// walks out of the root.
func Resolve(root, relPath string) (string, bool) {
	if relPath == "" {
		return "", false
	}
	abs := filepath.Join(root, relPath)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}
