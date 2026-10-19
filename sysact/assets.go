// Package sysact embeds the shipped action catalog.
package sysact

import "embed"

// Actions holds actions/*.yaml.
// 
//go:embed actions
var Actions embed.FS
