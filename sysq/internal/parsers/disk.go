package parsers

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"time"

	"howett.net/plist"
)

func init() {
	Register("disk_volumes", parseDiskVolumes)
	Register("du", parseDu)
	Register("ls_long", parseLsLong)
}

var (
	dfLine    = regexp.MustCompile(`^(.+?)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)%\s+(/.*)$`)
	mountLine = regexp.MustCompile(`^(.+?) on (/.*) \(([^)]*)\)$`)
)

// disk.volumes: df -k -P + mount (+ diskutil info for the boot volume name).
func parseDiskVolumes(in Input) ([]Row, error) {
	type mnt struct {
		fstype string
		opts   []string
	}
	mounts := map[string]mnt{}
	for _, l := range lines(in.Out("mount")) {
		m := mountLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		parts := strings.Split(m[3], ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		mounts[m[2]] = mnt{fstype: parts[0], opts: parts[1:]}
	}
	bootName := ""
	if info := in.Outputs["diskutil"]; len(bytes.TrimSpace(info)) > 0 {
		var d map[string]any
		if _, err := plist.Unmarshal(info, &d); err == nil {
			if s, ok := d["VolumeName"].(string); ok {
				bootName = s
			}
		}
	}
	var rows []Row
	for _, l := range lines(in.Out("df")) {
		m := dfLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		dev, mountPoint := m[1], m[6]
		mt := mounts[mountPoint]
		if mt.fstype == "devfs" || mt.fstype == "autofs" || strings.HasPrefix(dev, "map ") || dev == "devfs" {
			continue
		}
		size, _ := atoi(m[2])
		usedK, _ := atoi(m[3])
		avail, _ := atoi(m[4])
		size *= 1024
		used := usedK * 1024
		avail *= 1024
		if mt.fstype == "apfs" {
			// APFS volumes share a container: what is not free is used.
			used = size - avail
		}
		pct := 0.0
		if size > 0 {
			pct = round1(float64(used) / float64(size) * 100)
		}
		name := basename(mountPoint)
		if mountPoint == "/" {
			name = bootName
			if name == "" {
				name = "/"
			}
		}
		ro, local := false, false
		for _, o := range mt.opts {
			switch o {
			case "read-only":
				ro = true
			case "local":
				local = true
			}
		}
		rows = append(rows, Row{
			"name": name, "device": dev, "mount": mountPoint, "fstype": mt.fstype,
			"size": size, "used": used, "free": avail, "used_pct": pct,
			"mounted": true, "readonly": ro, "local": local,
			"internal": strings.HasPrefix(mountPoint, "/System/Volumes/"),
		})
	}
	return rows, nil
}

// disk.usage: du -k -d N. du prints children before parents; a time budget
// may cut it short, in which case the root total is missing.
func parseDu(in Input) ([]Row, error) {
	root, _ := in.Params["path"].(string)
	root = strings.TrimRight(root, "/")
	var rows []Row
	for _, l := range lines(in.Main()) {
		sz, p, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		n, ok := atoi(sz)
		if !ok {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		depth := int64(0)
		if rel != "" {
			depth = int64(strings.Count(rel, "/") + 1)
		}
		rows = append(rows, Row{"path": p, "name": basename(p), "size": n * 1024, "depth": depth})
	}
	return rows, nil
}

var lsLine = regexp.MustCompile(`^([bcdlsp-][rwxsStT-]{9})[@+.]?\s+(\d+)\s+(\S+)\s+(\S+)\s+(\d+|\d+,\s*\d+)\s+([A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4})\s(.+)$`)

// fs.list: ls -laT.
func parseLsLong(in Input) ([]Row, error) {
	dir, _ := in.Params["path"].(string)
	glob, _ := in.Params["glob"].(string)
	var rows []Row
	for _, l := range lines(in.Main()) {
		m := lsLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		name, target := m[7], ""
		if m[1][0] == 'l' {
			if a, b, ok := strings.Cut(name, " -> "); ok {
				name, target = a, b
			}
		}
		if name == "." || name == ".." {
			continue
		}
		if glob != "" {
			if ok, _ := path.Match(glob, name); !ok {
				continue
			}
		}
		kind := "other"
		switch m[1][0] {
		case 'd':
			kind = "dir"
		case '-':
			kind = "file"
		case 'l':
			kind = "link"
		}
		var size any
		if n, ok := atoi(m[5]); ok {
			size = n
		}
		var mod any
		if t, err := time.ParseInLocation("Jan _2 15:04:05 2006", strings.Join(strings.Fields(m[6]), " "), loc(in)); err == nil {
			mod = t
		} else if t, err := time.ParseInLocation("Jan 2 15:04:05 2006", strings.Join(strings.Fields(m[6]), " "), loc(in)); err == nil {
			mod = t
		}
		rows = append(rows, Row{
			"name": name, "path": path.Join(dir, name), "kind": kind, "size": size,
			"mode": m[1], "owner": m[3], "group": m[4], "modified": mod,
			"link_target": target, "hidden": strings.HasPrefix(name, "."),
		})
	}
	return rows, nil
}
