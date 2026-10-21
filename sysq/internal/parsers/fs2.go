package parsers

import (
	"bytes"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
	"howett.net/plist"
)

func init() {
	Register("find_ls_size", parseFindLsSize)
	Register("find_ls_recent", parseFindLsRecent)
	Register("stat_f", parseStatF)
	Register("shasum", parseShasum)
	Register("path_lines", parsePathLines)
	Register("crash_reports", parseCrashReports)
	Register("text_lines", parseTextLines)
	Register("brew_dirs", parseBrewDirs)
	Register("npm_dirs", parseNpmDirs)
	Register("disk_purgeable", parseDiskPurgeable)
	Register("iostat", parseIostat)
	Register("ollama_manifests", parseOllamaManifests)
}

// find -ls: inode blocks perms links owner group size Mon DD HH:MM|YYYY path
var findLs = regexp.MustCompile(`^\s*\d+\s+\d+\s+([bcdlsp-][rwxsStT-]{9})\S*\s+\d+\s+(\S+)\s+(\S+)\s+(\d+)\s+([A-Z][a-z]{2})\s+(\d{1,2})\s+(\d{1,2}:\d{2}|\d{4})\s(.+)$`)

type found struct {
	path     string
	owner    string
	size     int64
	modified any
}

func parseFindLsLines(in Input) []found {
	var out []found
	n := now(in)
	for _, l := range lines(in.Main()) {
		m := findLs.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		p := m[8]
		if m[1][0] == 'l' {
			p, _, _ = strings.Cut(p, " -> ")
		}
		size, _ := atoi(m[4])
		var mod any
		if strings.Contains(m[7], ":") {
			if t, err := time.ParseInLocation("Jan 2 15:04 2006", m[5]+" "+m[6]+" "+m[7]+" "+strconv.Itoa(n.Year()), loc(in)); err == nil {
				if t.After(n.Add(24 * time.Hour)) {
					t = t.AddDate(-1, 0, 0)
				}
				mod = t
			}
		} else if t, err := time.ParseInLocation("Jan 2 2006", m[5]+" "+m[6]+" "+m[7], loc(in)); err == nil {
			mod = t
		}
		out = append(out, found{path: p, owner: m[2], size: size, modified: mod})
	}
	return out
}

func limitOf(in Input, def int) int {
	if n, ok := in.Params["limit"].(int64); ok && n > 0 {
		return int(n)
	}
	return def
}

func foundRows(fs []found, limit int) []Row {
	if len(fs) > limit {
		fs = fs[:limit]
	}
	rows := make([]Row, len(fs))
	for i, f := range fs {
		rows[i] = Row{"path": f.path, "name": path.Base(f.path), "size": f.size, "modified": f.modified, "owner": f.owner}
	}
	return rows
}

// fs.large: biggest first.
func parseFindLsSize(in Input) ([]Row, error) {
	fs := parseFindLsLines(in)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].size > fs[j].size })
	return foundRows(fs, limitOf(in, 20)), nil
}

// fs.recent: newest first.
func parseFindLsRecent(in Input) ([]Row, error) {
	fs := parseFindLsLines(in)
	sort.SliceStable(fs, func(i, j int) bool {
		return table.Compare(fs[i].modified, fs[j].modified) > 0
	})
	return foundRows(fs, limitOf(in, 50)), nil
}

// fs.stat: stat -f "%HT\t%z\t%Sp\t%Su\t%Sg\t%m\t%a\t%B\t%N".
func parseStatF(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		f := strings.SplitN(l, "\t", 9)
		if len(f) != 9 {
			continue
		}
		ts := func(s string) any {
			if n, ok := atoi(s); ok {
				return time.Unix(n, 0).In(loc(in))
			}
			return nil
		}
		rows = append(rows, Row{"kind": strings.ToLower(f[0]), "size": optInt(f[1]), "mode": f[2], "owner": f[3], "group": f[4],
			"modified": ts(f[5]), "accessed": ts(f[6]), "created": ts(f[7]), "path": f[8]})
	}
	return rows, nil
}

// fs.hash: shasum -a 256 ("<hex>  <path>").
func parseShasum(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		h, p, ok := strings.Cut(l, "  ")
		if !ok || len(h) != 64 {
			continue
		}
		rows = append(rows, Row{"path": p, "sha256": h})
	}
	return rows, nil
}

// fs.search / fs.find_dirs: one path per line.
func parsePathLines(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		if !strings.HasPrefix(l, "/") {
			continue
		}
		rows = append(rows, Row{"path": l, "name": path.Base(l), "parent": path.Dir(l)})
	}
	return rows, nil
}

var crashName = regexp.MustCompile(`^(.+?)[-_](\d{4}-\d{2}-\d{2}-\d{6})(?:[._-][^.]*)?\.([a-z]+)$`)

// log.crashes: find -ls over DiagnosticReports; process and time from the name.
func parseCrashReports(in Input) ([]Row, error) {
	var rows []Row
	for _, f := range parseFindLsLines(in) {
		base := path.Base(f.path)
		m := crashName.FindStringSubmatch(base)
		if m == nil {
			continue
		}
		ts := f.modified
		if t, err := time.ParseInLocation("2006-01-02-150405", m[2], loc(in)); err == nil {
			ts = t
		}
		rows = append(rows, Row{"process": m[1], "ts": ts, "kind": m[3], "size": f.size, "path": f.path})
	}
	sort.SliceStable(rows, func(i, j int) bool { return table.Compare(rows[i]["ts"], rows[j]["ts"]) > 0 })
	return rows, nil
}

// log.file: numbered lines.
func parseTextLines(in Input) ([]Row, error) {
	var rows []Row
	if in.Main() == "" {
		return rows, nil
	}
	for i, l := range strings.Split(strings.TrimSuffix(in.Main(), "\n"), "\n") {
		rows = append(rows, Row{"line": int64(i + 1), "text": l})
	}
	return rows, nil
}

// pkg.brew: <prefix>/Cellar/<name>/<version> and Caskroom.
func parseBrewDirs(in Input) ([]Row, error) {
	var rows []Row
	counts := map[string]int64{}
	add := func(out, kind, marker string) {
		for _, l := range lines(out) {
			i := strings.Index(l, marker)
			if i < 0 {
				continue
			}
			rest := strings.Split(l[i+len(marker):], "/")
			if len(rest) != 2 || strings.HasPrefix(rest[1], ".") {
				continue
			}
			counts[kind+rest[0]]++
			rows = append(rows, Row{"name": rest[0], "kind": kind, "version": rest[1], "path": l})
		}
	}
	add(in.Out("cellar"), "formula", "/Cellar/")
	add(in.Out("caskroom"), "cask", "/Caskroom/")
	for _, r := range rows {
		r["versions"] = counts[r["kind"].(string)+r["name"].(string)]
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i]["name"].(string) < rows[j]["name"].(string) })
	return rows, nil
}

// pkg.npm_global: node_modules/<pkg> and node_modules/@scope/<pkg>.
func parseNpmDirs(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		i := strings.Index(l, "/node_modules/")
		if i < 0 {
			continue
		}
		parts := strings.Split(l[i+len("/node_modules/"):], "/")
		switch {
		case len(parts) == 1 && !strings.HasPrefix(parts[0], "@") && !strings.HasPrefix(parts[0], "."):
			rows = append(rows, Row{"name": parts[0], "scope": "", "path": l})
		case len(parts) == 2 && strings.HasPrefix(parts[0], "@"):
			rows = append(rows, Row{"name": parts[0] + "/" + parts[1], "scope": parts[0], "path": l})
		}
	}
	return rows, nil
}

// disk.purgeable: diskutil info -plist / + tmutil listlocalsnapshots /.
func parseDiskPurgeable(in Input) ([]Row, error) {
	var d map[string]any
	if _, err := plist.Unmarshal(bytes.TrimSpace(in.Outputs["info"]), &d); err != nil {
		return nil, err
	}
	num := func(keys ...string) any {
		for _, k := range keys {
			switch v := d[k].(type) {
			case uint64:
				return int64(v)
			case int64:
				return v
			}
		}
		return nil
	}
	var snaps any
	if out, ok := in.Outputs["snapshots"]; ok {
		n := int64(0)
		for _, l := range lines(string(out)) {
			if strings.HasPrefix(strings.TrimSpace(l), "com.apple.") {
				n++
			}
		}
		snaps = n
	}
	name, _ := d["VolumeName"].(string)
	return []Row{{
		"volume": name, "container_size": num("APFSContainerSize", "TotalSize"),
		"container_free": num("APFSContainerFree"), "volume_free": num("FreeSpace", "APFSContainerFree"),
		"purgeable": num("PurgeableSpace", "APFSPurgeableSpace"), "local_snapshots": snaps,
	}}, nil
}

// disk.io: iostat -d -c 2 -w 1; disk names on line 1, then KB/t tps MB/s
// triples; the last line is the one-second sample.
func parseIostat(in Input) ([]Row, error) {
	ls := lines(in.Main())
	if len(ls) < 3 {
		return nil, nil
	}
	disks := strings.Fields(ls[0])
	last := strings.Fields(ls[len(ls)-1])
	var rows []Row
	for i, d := range disks {
		if 3*i+2 >= len(last) {
			break
		}
		kbt, _ := atof(last[3*i])
		tps, _ := atof(last[3*i+1])
		mbs, _ := atof(last[3*i+2])
		rows = append(rows, Row{"disk": d, "kb_per_transfer": kbt, "tps": tps, "mb_per_s": mbs, "bytes_per_s": int64(mbs * 1048576)})
	}
	return rows, nil
}

var manifestLine = regexp.MustCompile(`^(.+?):"digest":"(sha256:[0-9a-f]+)","size":(\d+)$`)

// ai.model_files: grep -r -o over Ollama manifests; the manifest path is
// <dir>/<registry>/<namespace>/<model>/<tag>.
func parseOllamaManifests(in Input) ([]Row, error) {
	dir, _ := in.Params["dir"].(string)
	dir = strings.TrimRight(dir, "/") + "/"
	var rows []Row
	totals := map[string]int64{}
	biggest := map[string]int64{}
	for _, l := range lines(in.Main()) {
		m := manifestLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		rel := strings.TrimPrefix(m[1], dir)
		parts := strings.Split(rel, "/")
		if len(parts) < 2 {
			continue
		}
		tag := parts[len(parts)-1]
		name := parts[:len(parts)-1]
		if len(name) > 0 && name[0] == "registry.ollama.ai" {
			name = name[1:]
		}
		if len(name) > 1 && name[0] == "library" {
			name = name[1:]
		}
		model := strings.Join(name, "/") + ":" + tag
		size, _ := atoi(m[3])
		totals[model] += size
		if size > biggest[model] {
			biggest[model] = size
		}
		rows = append(rows, Row{"model": model, "digest": m[2], "size": size})
	}
	for _, r := range rows {
		model := r["model"].(string)
		size := r["size"].(int64)
		switch {
		case size == biggest[model]:
			r["role"] = "weights"
		case size < 4096:
			r["role"] = "config"
		default:
			r["role"] = "layer"
		}
		r["model_size"] = totals[model]
	}
	return rows, nil
}
