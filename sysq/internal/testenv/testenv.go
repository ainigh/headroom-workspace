// Package testenv builds a fixture-backed engine for tests: the shipped
// catalog, recorded output from tests/fixtures, a fixed clock and zone.
package testenv

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ainigh/headroom-workspace/sysq"
	"github.com/ainigh/headroom-workspace/sysq/internal/catalog"
	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/policy"
)

// Root is the sysq module directory.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// Now is the fixtures' capture time.
var Now = time.Date(2026, 10, 19, 10, 47, 2, 0, Loc())

// Loc is the fixtures' time zone.
func Loc() *time.Location {
	l, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.FixedZone("PDT", -7*3600)
	}
	return l
}

// Home is the fixtures' home directory.
const Home = "/Users/me"

// Engine returns a fixture-mode engine.
func Engine() (*engine.Engine, error) {
	cat, err := Catalog()
	if err != nil {
		return nil, err
	}
	return &engine.Engine{
		Catalog:  cat,
		Policy:   policy.Default(),
		Home:     Home,
		Fixtures: os.DirFS(filepath.Join(Root(), "tests", "fixtures")),
		Now:      func() time.Time { return Now },
		Loc:      Loc(),
	}, nil
}

// Catalog loads the shipped catalog.
func Catalog() (*catalog.Catalog, error) {
	sub, err := fs.Sub(sysq.Assets, "catalog")
	if err != nil {
		return nil, err
	}
	return catalog.Load(sub)
}
