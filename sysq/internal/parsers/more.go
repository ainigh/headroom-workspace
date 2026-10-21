package parsers

import (
	"regexp"
	"strings"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

func init() {
	Register("pmset_therm", parsePmsetTherm)
	Register("proc_info", parseProcInfo)
}

var (
	thermWarn  = regexp.MustCompile(`(?i)thermal warning level (?:set to|is) (\d+)`)
	speedLimit = regexp.MustCompile(`CPU_Speed_Limit\s*=\s*(\d+)`)
	perfNormal = regexp.MustCompile(`(?i)performance warning level (?:set to|is) 0\b`)
)

// sys.thermal: pmset -g therm. Apple silicon usually reports "No thermal
// warning level has been recorded"; a recorded level maps onto the four
// macOS thermal states.
func parsePmsetTherm(in Input) ([]Row, error) {
	out := in.Main()
	r := Row{"level": int64(0), "state": "nominal", "speed_limit": int64(100), "warning": ""}
	if m := speedLimit.FindStringSubmatch(out); m != nil {
		n, _ := atoi(m[1])
		r["speed_limit"] = n
		switch {
		case n < 50:
			r["level"], r["state"] = int64(3), "critical"
		case n < 80:
			r["level"], r["state"] = int64(2), "serious"
		case n < 100:
			r["level"], r["state"] = int64(1), "fair"
		}
	}
	// A recorded level of 0 is the normal level (macOS sets it back after
	// an event); only a non-zero level is a warning.
	if m := thermWarn.FindStringSubmatch(out); m != nil && m[1] != "0" {
		r["warning"] = "thermal warning level " + m[1]
		if r["level"].(int64) < 1 {
			r["level"], r["state"] = int64(1), "fair"
		}
	}
	for _, l := range lines(out) {
		if strings.Contains(strings.ToLower(l), "performance warning level") && !strings.Contains(l, "No ") && !perfNormal.MatchString(l) {
			r["warning"] = strings.TrimSpace(l)
		}
	}
	return []Row{r}, nil
}

// proc.info: ps for one pid + lsof -p (files and sockets as nested tables).
func parseProcInfo(in Input) ([]Row, error) {
	var base Row
	for _, l := range lines(in.Out("ps")) {
		f := strings.Fields(l)
		if len(f) < 12 {
			continue
		}
		pid, ok := atoi(f[0])
		if !ok {
			continue
		}
		ppid, _ := atoi(f[1])
		cpu, _ := atof(f[3])
		mp, _ := atof(f[4])
		rss, _ := atoi(f[5])
		var started any
		if t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[6:11], " "), loc(in)); err == nil {
			started = t
		}
		base = Row{"pid": pid, "ppid": ppid, "user": f[2], "cpu": cpu, "mem_pct": mp, "rss": rss * 1024,
			"started": started, "command": strings.Join(f[11:], " ")}
		break
	}
	if base == nil {
		return nil, nil
	}
	files := table.New([]table.Col{{Name: "fd", Type: table.Str}, {Name: "kind", Type: table.Str}, {Name: "path", Type: table.Path}})
	socks := table.New([]table.Col{{Name: "fd", Type: table.Str}, {Name: "family", Type: table.Str}, {Name: "proto", Type: table.Str}, {Name: "address", Type: table.Str}, {Name: "state", Type: table.Str}})
	var fd, kind, proto, name, state string
	flush := func() {
		if fd == "" {
			return
		}
		switch kind {
		case "IPv4", "IPv6":
			socks.Append(fd, kind, proto, name, state)
		case "unix", "KQUEUE", "PIPE", "NPOLICY", "systm", "":
		default:
			if strings.HasPrefix(name, "/") {
				files.Append(fd, kind, name)
			}
		}
		fd, kind, proto, name, state = "", "", "", "", ""
	}
	for _, l := range lines(in.Out("lsof")) {
		switch l[0] {
		case 'f':
			flush()
			fd = l[1:]
		case 't':
			kind = l[1:]
		case 'P':
			proto = l[1:]
		case 'n':
			name = l[1:]
		case 'T':
			if strings.HasPrefix(l[1:], "ST=") {
				state = strings.TrimPrefix(l[1:], "ST=")
			}
		}
	}
	flush()
	base["open_files"] = files
	base["sockets"] = socks
	return []Row{base}, nil
}
