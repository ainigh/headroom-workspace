// Package protect refuses system and security-critical locations at prepare
// time (design §27). protected_paths.yaml can add locations, never remove the
// shipped ones.
package protect

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed protected_paths.yaml
var defaults []byte

// Rules are exact paths (the folder itself) and trees (it and everything inside).
type Rules struct {
	Version int      `yaml:"version"`
	Exact   []string `yaml:"exact"`
	Trees   []string `yaml:"trees"`
	home    string
}

// Load reads the shipped rules plus the user's file (additions only).
func Load(userFile, home string) (*Rules, error) {
	var r Rules
	if err := yaml.Unmarshal(defaults, &r); err != nil {
		return nil, err
	}
	r.home = home
	if data, err := os.ReadFile(userFile); err == nil {
		var u Rules
		if err := yaml.Unmarshal(data, &u); err != nil {
			return &r, fmt.Errorf("%s: %w (shipped rules still apply)", userFile, err)
		}
		r.Exact = append(r.Exact, u.Exact...)
		r.Trees = append(r.Trees, u.Trees...)
	}
	return &r, nil
}

func (r *Rules) expand(p string) string {
	if p == "~" {
		return r.home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(r.home, p[2:])
	}
	return p
}

func resolve(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	// Resolve the existing parent so a not-yet-existing leaf can't hide a link.
	dir, base := filepath.Split(p)
	if real, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(real, base)
	}
	return p
}

// Check returns the cleaned absolute path, or an error when it is protected.
func (r *Rules) Check(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	p = r.expand(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be absolute or start with ~")
	}
	clean := filepath.Clean(p)
	// Compared case-folded: APFS and HFS+ volumes are case-insensitive by
	// default, so ~/documents names ~/Documents. (On a case-sensitive volume
	// this refuses a little more, never less.)
	fold := strings.ToLower
	for _, cand := range []string{fold(clean), fold(resolve(clean))} {
		for _, e := range r.Exact {
			ex := filepath.Clean(r.expand(e))
			if cand == fold(ex) || cand == fold(resolve(ex)) {
				return "", fmt.Errorf("%s is protected", p)
			}
		}
		for _, t := range r.Trees {
			tr := filepath.Clean(r.expand(t))
			for _, root := range []string{fold(tr), fold(resolve(tr))} {
				if cand == root || strings.HasPrefix(cand, root+"/") || root == "/" {
					return "", fmt.Errorf("%s is inside protected %s", p, t)
				}
			}
		}
	}
	return clean, nil
}
