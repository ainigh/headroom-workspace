// Package testhome makes a stand-in home directory for tests.
//
// It can't be t.TempDir(): on macOS that lives under /private/var/folders,
// inside the protected /private/var tree, so every path in the fake home
// would (correctly) be refused. The directory goes next to the test's
// package instead and is removed afterwards.
package testhome

import (
	"os"
	"path/filepath"
	"testing"
)

// New returns a fresh, symlink-resolved directory to use as $HOME.
func New(t testing.TB) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(wd, ".testhome-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
