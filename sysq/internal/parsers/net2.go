package parsers

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/pkg/argv"
)

func init() {
	Register("net_wifi", parseNetWifi)
	Register("scutil_dns", parseScutilDNS)
	Register("netstat_routes", parseNetstatRoutes)
	Register("firewall", parseFirewall)
	Register("netstat_ib", parseNetstatIB)
	Register("nettop_csv", parseNettopCSV)
	Register("git_status", parseGitStatus)
	Register("git_log", parseGitLog)
	Register("git_branches", parseGitBranches)
	Register("ioreg_gpu", parseIoregGPU)
	Register("top_energy", parseTopEnergy)
	Register("ollama_ps", parseOllamaPs)
	Register("lms_ps", parseLmsPs)
}

// net.wifi: networksetup -getairportnetwork en0 + -getinfo Wi-Fi.
func parseNetWifi(in Input) ([]Row, error) {
	r := Row{"ssid": "", "connected": false, "ip": "", "router": "", "mac": ""}
	for _, l := range lines(in.Out("network")) {
		if _, v, ok := strings.Cut(l, "Current Wi-Fi Network: "); ok {
			r["ssid"] = strings.TrimSpace(v)
			r["connected"] = true
		}
	}
	info := kv(in.Out("info"), ":")
	if ip := info["IP address"]; ip != "" && ip != "none" {
		r["ip"] = ip
		r["connected"] = true
	}
	r["router"] = info["Router"]
	r["mac"] = info["Wi-Fi ID"]
	return []Row{r}, nil
}

var (
	resolverHead = regexp.MustCompile(`^resolver #(\d+)`)
	dnsField     = regexp.MustCompile(`^\s+([a-z_ ]+?)(?:\[\d+\])?\s*:\s*(.*)$`)
)

// net.dns: scutil --dns. Resolvers after "for scoped queries" are scoped.
func parseScutilDNS(in Input) ([]Row, error) {
	var rows []Row
	scoped := false
	type res struct {
		n                     int64
		domain, search, iface string
		servers               []string
	}
	var cur *res
	flush := func() {
		if cur == nil {
			return
		}
		servers := cur.servers
		if len(servers) == 0 {
			servers = []string{""}
		}
		for _, s := range servers {
			rows = append(rows, Row{"resolver": cur.n, "scoped": scoped, "domain": cur.domain, "search": cur.search, "nameserver": s, "interface": cur.iface})
		}
		cur = nil
	}
	for _, l := range strings.Split(in.Main(), "\n") {
		if strings.HasPrefix(l, "DNS configuration (for scoped queries)") {
			flush()
			scoped = true
			continue
		}
		if m := resolverHead.FindStringSubmatch(l); m != nil {
			flush()
			n, _ := atoi(m[1])
			cur = &res{n: n}
			continue
		}
		m := dnsField.FindStringSubmatch(l)
		if m == nil || cur == nil {
			continue
		}
		switch strings.TrimSpace(m[1]) {
		case "nameserver":
			cur.servers = append(cur.servers, m[2])
		case "domain":
			cur.domain = m[2]
		case "search domain":
			if cur.search != "" {
				cur.search += " "
			}
			cur.search += m[2]
		case "if_index":
			if i := strings.Index(m[2], "("); i >= 0 {
				cur.iface = strings.Trim(m[2][i:], "()")
			}
		}
	}
	flush()
	return rows, nil
}

// net.routes: netstat -rn -f inet.
func parseNetstatRoutes(in Input) ([]Row, error) {
	var rows []Row
	started := false
	for _, l := range lines(in.Main()) {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == "Destination" {
			started = true
			continue
		}
		if !started || len(f) < 4 {
			continue
		}
		exp := ""
		if len(f) >= 5 {
			exp = f[4]
		}
		rows = append(rows, Row{"destination": f[0], "gateway": f[1], "flags": f[2], "interface": f[3], "expire": exp})
	}
	return rows, nil
}

var fwState = regexp.MustCompile(`State = (\d)`)

// net.firewall: socketfilterfw --getglobalstate / --getstealthmode.
func parseFirewall(in Input) ([]Row, error) {
	st := in.Out("state")
	r := Row{"enabled": strings.Contains(st, "enabled") || strings.Contains(st, "blocking"), "state": nil, "stealth": nil}
	if m := fwState.FindStringSubmatch(st); m != nil {
		n, _ := atoi(m[1])
		r["state"] = n
		r["enabled"] = n > 0
	}
	if s := strings.ToLower(in.Out("stealth")); s != "" {
		r["stealth"] = strings.Contains(s, "enabled") || strings.Contains(s, " is on")
	}
	return []Row{r}, nil
}

// net.netstat: netstat -ib, link-layer rows only. Counters are the last
// seven fields (Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll); Address may be empty.
func parseNetstatIB(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		f := strings.Fields(l)
		if len(f) < 10 || !strings.HasPrefix(f[2], "<Link#") {
			continue
		}
		n := len(f)
		name := f[0]
		up := !strings.HasSuffix(name, "*")
		name = strings.TrimSuffix(name, "*")
		rows = append(rows, Row{
			"name": name, "up": up, "mtu": optInt(f[1]),
			"ipkts": optInt(f[n-7]), "ierrs": optInt(f[n-6]), "ibytes": optInt(f[n-5]),
			"opkts": optInt(f[n-4]), "oerrs": optInt(f[n-3]), "obytes": optInt(f[n-2]),
		})
	}
	return rows, nil
}

// net.by_process: nettop -P -L 1 -J bytes_in,bytes_out -x (CSV:
// time,process.pid,bytes_in,bytes_out,).
func parseNettopCSV(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		f := strings.Split(l, ",")
		if len(f) < 4 || f[0] == "time" {
			continue
		}
		name := f[1]
		i := strings.LastIndexByte(name, '.')
		if i < 0 {
			continue
		}
		pid, ok := atoi(name[i+1:])
		if !ok {
			continue
		}
		bin, _ := atoi(f[2])
		bout, _ := atoi(f[3])
		rows = append(rows, Row{"command": name[:i], "pid": pid, "bytes_in": bin, "bytes_out": bout, "bytes": bin + bout})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i]["bytes"].(int64) > rows[j]["bytes"].(int64) })
	return rows, nil
}

var gitAB = regexp.MustCompile(`^# branch\.ab \+(\d+) -(\d+)`)

// git.status: git status --porcelain=v2 --branch.
func parseGitStatus(in Input) ([]Row, error) {
	p, _ := in.Params["path"].(string)
	r := Row{"path": p, "branch": "", "upstream": "", "ahead": int64(0), "behind": int64(0)}
	changed, untracked := int64(0), int64(0)
	for _, l := range lines(in.Main()) {
		switch {
		case strings.HasPrefix(l, "# branch.head "):
			r["branch"] = strings.TrimPrefix(l, "# branch.head ")
		case strings.HasPrefix(l, "# branch.upstream "):
			r["upstream"] = strings.TrimPrefix(l, "# branch.upstream ")
		case strings.HasPrefix(l, "# branch.ab "):
			if m := gitAB.FindStringSubmatch(l); m != nil {
				r["ahead"], r["behind"] = optInt(m[1]), optInt(m[2])
			}
		case strings.HasPrefix(l, "? "):
			untracked++
		case strings.HasPrefix(l, "1 "), strings.HasPrefix(l, "2 "), strings.HasPrefix(l, "u "):
			changed++
		}
	}
	r["changed"], r["untracked"] = changed, untracked
	r["clean"] = changed == 0 && untracked == 0
	return []Row{r}, nil
}

// git.log: %H\t%an\t%aI\t%s.
func parseGitLog(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		f := strings.SplitN(l, "\t", 4)
		if len(f) != 4 {
			continue
		}
		rows = append(rows, Row{"commit": f[0], "author": f[1], "date": isoTime(f[2]), "subject": f[3]})
	}
	return rows, nil
}

var trackRe = regexp.MustCompile(`(ahead|behind) (\d+)`)

// git.branches: for-each-ref name\tsha\tupstream\ttrack\tdate.
func parseGitBranches(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		f := strings.Split(l, "\t")
		if len(f) != 5 {
			continue
		}
		r := Row{"branch": f[0], "commit": f[1], "upstream": f[2], "ahead": int64(0), "behind": int64(0), "committed": isoTime(f[4])}
		for _, m := range trackRe.FindAllStringSubmatch(f[3], -1) {
			r[m[1]] = optInt(m[2])
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func isoTime(s string) any {
	if t, err := parseRFC3339(s); err == nil {
		return t
	}
	return nil
}

var (
	gpuModel = regexp.MustCompile(`"model" = "([^"]+)"`)
	gpuCores = regexp.MustCompile(`"gpu-core-count" = (\d+)`)
	gpuStat  = regexp.MustCompile(`"(Device Utilization %|Renderer Utilization %|Tiler Utilization %|In use system memory|Alloc system memory)"=(\d+)`)
)

// gpu.stats: ioreg -r -d 1 -c IOAccelerator PerformanceStatistics.
func parseIoregGPU(in Input) ([]Row, error) {
	out := in.Main()
	stats := gpuStat.FindAllStringSubmatch(out, -1)
	if len(stats) == 0 {
		return nil, nil
	}
	r := Row{"model": "", "cores": nil}
	if m := gpuModel.FindStringSubmatch(out); m != nil {
		r["model"] = m[1]
	}
	if m := gpuCores.FindStringSubmatch(out); m != nil {
		r["cores"] = optInt(m[1])
	}
	for _, m := range stats {
		v, _ := strconv.ParseFloat(m[2], 64)
		switch m[1] {
		case "Device Utilization %":
			r["utilization"] = v
		case "Renderer Utilization %":
			r["renderer"] = v
		case "Tiler Utilization %":
			r["tiler"] = v
		case "In use system memory":
			r["in_use_mem"] = int64(v)
		case "Alloc system memory":
			r["alloc_mem"] = int64(v)
		}
	}
	return []Row{r}, nil
}

// proc.energy: last sample of top -stats pid,command,power,cpu.
func parseTopEnergy(in Input) ([]Row, error) {
	ls := lines(in.Main())
	start := -1
	for i, l := range ls {
		if topHeader.MatchString(l) {
			start = i
		}
	}
	if start < 0 {
		return nil, nil
	}
	var rows []Row
	for _, l := range ls[start+1:] {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		pid, ok := atoi(f[0])
		if !ok {
			continue
		}
		n := len(f)
		power, ok1 := atof(f[n-2])
		cpu, ok2 := atof(f[n-1])
		if !ok1 || !ok2 {
			continue
		}
		rows = append(rows, Row{"pid": pid, "command": strings.Join(f[1:n-2], " "), "power": power, "cpu": cpu})
	}
	return rows, nil
}

var gpuPct = regexp.MustCompile(`(\d+)% GPU`)

// ai.ollama_ps: columns located from the header (NAME ID SIZE PROCESSOR [CONTEXT] UNTIL).
func parseOllamaPs(in Input) ([]Row, error) {
	ls := lines(in.Main())
	if len(ls) == 0 || !strings.HasPrefix(ls[0], "NAME") {
		return nil, nil
	}
	head := ls[0]
	cols := []string{"NAME", "ID", "SIZE", "PROCESSOR", "CONTEXT", "UNTIL"}
	pos := map[string]int{}
	for _, c := range cols {
		if i := strings.Index(head, c); i >= 0 {
			pos[c] = i
		}
	}
	field := func(l, c string) string {
		start, ok := pos[c]
		if !ok || start >= len(l) {
			return ""
		}
		end := len(l)
		for _, o := range cols {
			if p, ok := pos[o]; ok && p > start && p < end {
				end = p
			}
		}
		if end > len(l) {
			end = len(l)
		}
		return strings.TrimSpace(l[start:end])
	}
	var rows []Row
	for _, l := range ls[1:] {
		r := Row{"model": field(l, "NAME"), "id": field(l, "ID"), "processor": field(l, "PROCESSOR"), "until": field(l, "UNTIL")}
		if n, err := argv.ParseBytes(strings.ReplaceAll(field(l, "SIZE"), " ", "")); err == nil {
			r["size"] = n
		}
		if m := gpuPct.FindStringSubmatch(r["processor"].(string)); m != nil {
			r["gpu_pct"] = optInt(m[1])
		} else if strings.Contains(r["processor"].(string), "CPU") {
			r["gpu_pct"] = int64(0)
		}
		r["context"] = optInt(field(l, "CONTEXT"))
		rows = append(rows, r)
	}
	return rows, nil
}

// ai.lmstudio_ps: lms ps --json.
func parseLmsPs(in Input) ([]Row, error) {
	var items []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(in.Main())), &items); err != nil {
		return nil, err
	}
	var rows []Row
	for _, it := range items {
		str := func(keys ...string) string {
			for _, k := range keys {
				if s, ok := it[k].(string); ok && s != "" {
					return s
				}
			}
			return ""
		}
		r := Row{"model": str("identifier", "modelKey", "displayName"), "path": str("path"), "type": str("type")}
		if f, ok := it["sizeBytes"].(float64); ok {
			r["size"] = int64(f)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func parseRFC3339(s string) (time.Time, error) { return time.Parse(time.RFC3339, strings.TrimSpace(s)) }
