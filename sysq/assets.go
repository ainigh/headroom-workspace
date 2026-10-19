// Package sysq embeds the shipped catalog, recipes, sandbox profiles and JSON
// Schemas so a single binary carries them. Files under
// ~/Library/Application Support/sysq/{catalog,recipes} overlay these.
package sysq

import "embed"

// Assets holds the shipped defaults.
//
//go:embed catalog sandbox recipes tests/fixtures
var Assets embed.FS
