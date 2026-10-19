package parsers

import (
	"regexp"
	"strings"
)

func init() {
	Register("sys_info", parseSysInfo)
	Register("sys_memory", parseSysMemory)
	Register("sys_cpu", parseSysCPU)
}

// sys.info: sw_vers + sysctl.
func parseSysInfo(in Input) ([]Row, error) {
	sw := kv(in.Out("sw_vers"), ":")
	sc := kv(in.Out("sysctl"), ":")
	r := Row{
		"os":         sw["ProductName"],
		"os_version": sw["ProductVersion"],
		"build":      sw["BuildVersion"],
		"model":      sc["hw.model"],
		"chip":       sc["machdep.cpu.brand_string"],
		"memory":     optInt(sc["hw.memsize"]),
		"cpus":       optInt(sc["hw.ncpu"]),
		"p_cores":    optInt(sc["hw.perflevel0.physicalcpu"]),
		"e_cores":    optInt(sc["hw.perflevel1.physicalcpu"]),
		"kernel":     sc["kern.osrelease"],
	}
	return []Row{r}, nil
}

var (
	vmPageSize = regexp.MustCompile(`page size of (\d+) bytes`)
	swapUsage  = regexp.MustCompile(`total = ([0-9.]+[KMGT]?)\s+used = ([0-9.]+[KMGT]?)\s+free = ([0-9.]+[KMGT]?)`)
	freePct    = regexp.MustCompile(`System-wide memory free percentage: (\d+)%`)
)

// sys.memory: vm_stat + sysctl + memory_pressure, computed the way Activity
// Monitor presents it (used = app + wired + compressed).
func parseSysMemory(in Input) ([]Row, error) {
	vm := in.Out("vm_stat")
	page := int64(16384)
	if m := vmPageSize.FindStringSubmatch(vm); m != nil {
		page, _ = atoi(m[1])
	}
	pages := map[string]int64{}
	for k, v := range kv(vm, ":") {
		if n, ok := atoi(v); ok {
			pages[strings.Trim(k, `"`)] = n
		}
	}
	sc := kv(in.Out("sysctl"), ":")
	total, _ := atoi(sc["hw.memsize"])
	wired := pages["Pages wired down"] * page
	compressed := pages["Pages occupied by compressor"] * page
	purgeable := pages["Pages purgeable"] * page
	app := pages["Anonymous pages"]*page - purgeable
	if app < 0 {
		app = 0
	}
	cached := pages["File-backed pages"]*page + purgeable
	used := app + wired + compressed
	r := Row{
		"total":      total,
		"used":       used,
		"available":  max64(total-used, 0),
		"free":       pages["Pages free"] * page,
		"app":        app,
		"wired":      wired,
		"compressed": compressed,
		"cached":     cached,
		"purgeable":  purgeable,
		"inactive":   pages["Pages inactive"] * page,
		"page_size":  page,
		"pageouts":   pages["Pageouts"],
		"swapins":    pages["Swapins"],
		"swapouts":   pages["Swapouts"],
	}
	if m := swapUsage.FindStringSubmatch(sc["vm.swapusage"]); m != nil {
		t, _ := suffixBytes(m[1])
		u, _ := suffixBytes(m[2])
		r["swap_total"], r["swap_used"] = t, u
	}
	switch sc["kern.memorystatus_vm_pressure_level"] {
	case "1":
		r["pressure"] = "normal"
	case "2":
		r["pressure"] = "warn"
	case "4":
		r["pressure"] = "critical"
	}
	if m := freePct.FindStringSubmatch(in.Out("memory_pressure")); m != nil {
		f, _ := atof(m[1])
		r["free_pct"] = f
		r["pressure_pct"] = 100 - f
	}
	return []Row{r}, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

var (
	cpuUsage = regexp.MustCompile(`CPU usage: ([0-9.]+)% user, ([0-9.]+)% sys, ([0-9.]+)% idle`)
	procs    = regexp.MustCompile(`Processes: (\d+) total.*?(\d+) threads`)
	loadAvg  = regexp.MustCompile(`\{\s*([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)\s*\}`)
)

// sys.cpu: sysctl + the second sample of top's header.
func parseSysCPU(in Input) ([]Row, error) {
	sc := kv(in.Out("sysctl"), ":")
	r := Row{
		"brand":   sc["machdep.cpu.brand_string"],
		"cores":   optInt(sc["hw.ncpu"]),
		"p_cores": optInt(sc["hw.perflevel0.logicalcpu"]),
		"e_cores": optInt(sc["hw.perflevel1.logicalcpu"]),
	}
	if m := loadAvg.FindStringSubmatch(sc["vm.loadavg"]); m != nil {
		r["load1"], r["load5"], r["load15"] = optFloat(m[1]), optFloat(m[2]), optFloat(m[3])
	}
	top := in.Out("top")
	if all := cpuUsage.FindAllStringSubmatch(top, -1); len(all) > 0 {
		m := all[len(all)-1]
		u, _ := atof(m[1])
		s, _ := atof(m[2])
		i, _ := atof(m[3])
		r["user_pct"], r["sys_pct"], r["idle_pct"] = u, s, i
		r["load_pct"] = round1(u + s)
	}
	if all := procs.FindAllStringSubmatch(top, -1); len(all) > 0 {
		m := all[len(all)-1]
		r["processes"], r["threads"] = optInt(m[1]), optInt(m[2])
	}
	return []Row{r}, nil
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
