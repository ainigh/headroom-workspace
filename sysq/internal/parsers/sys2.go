package parsers

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"howett.net/plist"
)

func init() {
	Register("sys_uptime", parseSysUptime)
	Register("sys_power", parseSysPower)
	Register("tm_status", parseTmStatus)
	Register("crontab", parseCrontab)
	Register("sec_status", parseSecStatus)
	Register("dscl_users", parseDsclUsers)
	Register("su_history", parseSuHistory)
	Register("sp_applications", parseSpApplications)
	Register("plist_kv", parsePlistKV)
	Register("tool_versions", parseToolVersions)
	Register("battery_state", parseBatteryState)
	Register("cpu_cores", parseCPUCores)
	Register("thermal_state", parseThermalState)
	Register("powermetrics_power", parsePowermetricsPower)
	Register("powermetrics_thermal", parsePowermetricsThermal)
}

func now(in Input) time.Time {
	if in.Now.IsZero() {
		return time.Now()
	}
	return in.Now
}

var bootSec = regexp.MustCompile(`sec = (\d+)`)

// sys.uptime: sysctl -n kern.boottime ("{ sec = 1759676472, usec = 0 } Sun Oct  5 ...").
func parseSysUptime(in Input) ([]Row, error) {
	m := bootSec.FindStringSubmatch(in.Main())
	if m == nil {
		return nil, nil
	}
	sec, _ := atoi(m[1])
	booted := time.Unix(sec, 0).In(loc(in))
	return []Row{{"booted": booted, "uptime": now(in).Sub(booted).Seconds()}}, nil
}

var (
	battSource = regexp.MustCompile(`Now drawing from '([^']+)'`)
	battLine   = regexp.MustCompile(`(\d+)%;\s*([^;]+?);\s*(?:\(no estimate\)|(\d+):(\d+) remaining)?`)
)

type battInfo struct {
	source     string
	hasBattery bool
	percent    any
	state      string
	remaining  any
}

// parseBatt reads `pmset -g batt`.
func parseBatt(out string) battInfo {
	b := battInfo{source: "ac"}
	if m := battSource.FindStringSubmatch(out); m != nil {
		switch {
		case strings.Contains(m[1], "Battery"):
			b.source = "battery"
		case strings.Contains(m[1], "UPS"):
			b.source = "ups"
		}
	}
	if m := battLine.FindStringSubmatch(out); m != nil && strings.Contains(out, "InternalBattery") {
		b.hasBattery = true
		b.percent = optInt(m[1])
		b.state = strings.TrimSpace(m[2])
		if m[3] != "" {
			h, _ := atoi(m[3])
			mm, _ := atoi(m[4])
			if h > 0 || mm > 0 {
				b.remaining = float64(h*3600 + mm*60)
			}
		}
	}
	return b
}

// sys.power: pmset -g batt + pmset -g (lowpowermode).
func parseSysPower(in Input) ([]Row, error) {
	b := parseBatt(in.Out("batt"))
	low := false
	for _, l := range lines(in.Out("settings")) {
		f := strings.Fields(l)
		if len(f) >= 2 && (f[0] == "lowpowermode" || f[0] == "powermode") && f[1] != "0" {
			low = true
		}
	}
	return []Row{{
		"source": b.source, "has_battery": b.hasBattery, "percent": b.percent, "state": b.state,
		"remaining": b.remaining, "low_power_mode": low,
	}}, nil
}

var (
	tmKV     = regexp.MustCompile(`^\s*"?([A-Za-z]+)"?\s*=\s*"?([^";]*)"?;`)
	tmBackup = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}-\d{6})`)
)

// tm.status: tmutil status (an old-style plist dictionary) + tmutil latestbackup.
func parseTmStatus(in Input) ([]Row, error) {
	r := Row{"running": false, "phase": "", "percent": nil, "last_backup": nil, "hours_since_last_backup": nil}
	for _, l := range lines(in.Out("status")) {
		m := tmKV.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		switch m[1] {
		case "Running":
			r["running"] = m[2] == "1"
		case "BackupPhase":
			r["phase"] = m[2]
		case "Percent":
			if f, ok := atof(m[2]); ok && f >= 0 {
				r["percent"] = round1(f * 100)
			}
		}
	}
	all := tmBackup.FindAllString(in.Out("latest"), -1)
	if len(all) > 0 {
		if t, err := time.ParseInLocation("2006-01-02-150405", all[len(all)-1], loc(in)); err == nil {
			r["last_backup"] = t
			r["hours_since_last_backup"] = round1(now(in).Sub(t).Hours())
		}
	}
	return []Row{r}, nil
}

// svc.cron: crontab -l.
func parseCrontab(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "crontab:") {
			continue
		}
		f := strings.Fields(t)
		if strings.HasPrefix(f[0], "@") {
			rows = append(rows, Row{"schedule": f[0], "command": strings.Join(f[1:], " ")})
			continue
		}
		if len(f) < 6 || strings.Contains(f[0], "=") {
			continue // environment assignments
		}
		rows = append(rows, Row{"schedule": strings.Join(f[:5], " "), "command": strings.Join(f[5:], " ")})
	}
	return rows, nil
}

// sec.status: csrutil status, fdesetup status, spctl --status.
func parseSecStatus(in Input) ([]Row, error) {
	first := func(id string) string {
		ls := lines(in.Out(id))
		if len(ls) == 0 {
			return ""
		}
		return strings.TrimSpace(ls[0])
	}
	sip, fv, gk := first("csrutil"), first("fdesetup"), first("spctl")
	opt := func(raw string, yes func(string) bool) any {
		if raw == "" {
			return nil
		}
		return yes(strings.ToLower(raw))
	}
	return []Row{{
		"sip":            opt(sip, func(s string) bool { return strings.Contains(s, "enabled") && !strings.Contains(s, "disabled") }),
		"filevault":      opt(fv, func(s string) bool { return strings.Contains(s, "is on") }),
		"gatekeeper":     opt(gk, func(s string) bool { return strings.Contains(s, "assessments enabled") }),
		"sip_raw":        sip,
		"filevault_raw":  fv,
		"gatekeeper_raw": gk,
	}}, nil
}

// users.list: dscl . list /Users UniqueID.
func parseDsclUsers(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		uid, ok := atoi(f[1])
		if !ok {
			continue
		}
		rows = append(rows, Row{"name": f[0], "uid": uid, "system": strings.HasPrefix(f[0], "_") || uid < 500})
	}
	return rows, nil
}

// sys.updates: softwareupdate --history (fixed-width columns from the header).
func parseSuHistory(in Input) ([]Row, error) {
	ls := lines(in.Main())
	var rows []Row
	vi, di := -1, -1
	for _, l := range ls {
		if vi < 0 {
			if strings.HasPrefix(l, "Display Name") {
				vi = strings.Index(l, "Version")
				di = strings.Index(l, "Date")
			}
			continue
		}
		if strings.HasPrefix(l, "---") || len(l) <= di {
			continue
		}
		name := strings.TrimSpace(l[:vi])
		version := strings.TrimSpace(l[vi:di])
		date := strings.TrimSpace(l[di:])
		var ts any
		for _, layout := range []string{"01/02/2006, 15:04:05", "02/01/2006, 15:04:05", "2006-01-02, 15:04:05"} {
			if t, err := time.ParseInLocation(layout, date, loc(in)); err == nil {
				ts = t
				break
			}
		}
		rows = append(rows, Row{"name": name, "version": version, "installed": ts})
	}
	return rows, nil
}

// apps.installed: system_profiler SPApplicationsDataType -json.
func parseSpApplications(in Input) ([]Row, error) {
	var doc struct {
		Apps []struct {
			Name         string `json:"_name"`
			Version      string `json:"version"`
			Path         string `json:"path"`
			ObtainedFrom string `json:"obtained_from"`
			LastModified string `json:"lastModified"`
			Arch         string `json:"arch_kind"`
		} `json:"SPApplicationsDataType"`
	}
	if err := json.Unmarshal([]byte(in.Main()), &doc); err != nil {
		return nil, err
	}
	var rows []Row
	for _, a := range doc.Apps {
		var mod any
		if t, err := time.Parse(time.RFC3339, a.LastModified); err == nil {
			mod = t.In(loc(in))
		}
		arch := strings.TrimPrefix(a.Arch, "arch_")
		rows = append(rows, Row{"name": a.Name, "version": a.Version, "path": a.Path,
			"obtained_from": a.ObtainedFrom, "last_modified": mod, "arch": arch})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i]["name"].(string)) < strings.ToLower(rows[j]["name"].(string))
	})
	return rows, nil
}

// fs.meta / prefs.read: a plist dictionary as key/value rows.
func parsePlistKV(in Input) ([]Row, error) {
	data := in.Main()
	if strings.TrimSpace(data) == "" {
		return nil, nil
	}
	var d map[string]any
	if _, err := plist.Unmarshal([]byte(data), &d); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows []Row
	for _, k := range keys {
		v, typ := plistText(d[k])
		rows = append(rows, Row{"key": k, "value": v, "type": typ})
	}
	return rows, nil
}

func plistText(v any) (string, string) {
	switch x := v.(type) {
	case string:
		return x, "string"
	case bool:
		return strconv.FormatBool(x), "bool"
	case uint64:
		return strconv.FormatUint(x, 10), "int"
	case int64:
		return strconv.FormatInt(x, 10), "int"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), "float"
	case time.Time:
		return x.Format(time.RFC3339), "date"
	case []byte:
		return fmt.Sprintf("<%d bytes>", len(x)), "data"
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			s, _ := plistText(e)
			parts = append(parts, s)
		}
		s := strings.Join(parts, ", ")
		if len(s) > 500 {
			s = s[:500] + "…"
		}
		return s, "array"
	case map[string]any:
		b, _ := json.Marshal(x)
		s := string(b)
		if len(s) > 500 {
			s = s[:500] + "…"
		}
		return s, "dict"
	}
	return fmt.Sprint(v), "other"
}

var versionRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// dev.tools: "<tool> --version" outputs, one optional command per tool.
func parseToolVersions(in Input) ([]Row, error) {
	ids := make([]string, 0, len(in.Outputs))
	for id := range in.Outputs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rows []Row
	for _, id := range ids {
		out := strings.TrimSpace(in.Out(id))
		if out == "" {
			continue
		}
		first := strings.SplitN(out, "\n", 2)[0]
		src := out
		if id == "swift" {
			// "swift-driver version: 1.x Apple Swift version 6.2 (...)": the language version
			if i := strings.Index(out, "Swift version"); i >= 0 {
				src = out[i:]
			}
		}
		rows = append(rows, Row{"tool": id, "version": versionRe.FindString(src), "raw": first})
	}
	return rows, nil
}

var ioregKV = regexp.MustCompile(`"([A-Za-z]+)" = (-?\d+|Yes|No)\b`)

// battery.state: pmset -g batt + ioreg -r -c AppleSmartBattery.
func parseBatteryState(in Input) ([]Row, error) {
	b := parseBatt(in.Out("batt"))
	r := Row{"has_battery": b.hasBattery, "percent": b.percent, "state": b.state, "remaining": b.remaining,
		"plugged": b.source == "ac", "charging": strings.HasPrefix(b.state, "charging")}
	kv := map[string]string{}
	for _, m := range ioregKV.FindAllStringSubmatch(in.Out("ioreg"), -1) {
		if _, seen := kv[m[1]]; !seen {
			kv[m[1]] = m[2]
		}
	}
	num := func(k string) (float64, bool) {
		s, ok := kv[k]
		if !ok {
			return 0, false
		}
		// ioreg prints negative amperage as a wrapped unsigned 64-bit value.
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return float64(int64(u)), true
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	if len(kv) > 0 {
		r["has_battery"] = true
		if v, ok := kv["ExternalConnected"]; ok {
			r["plugged"] = v == "Yes"
		}
		if v, ok := kv["IsCharging"]; ok {
			r["charging"] = v == "Yes"
		}
		if n, ok := num("CycleCount"); ok {
			r["cycle_count"] = int64(n)
		}
		design, ok1 := num("DesignCapacity")
		maxc, ok2 := num("AppleRawMaxCapacity")
		if !ok2 {
			maxc, ok2 = num("NominalChargeCapacity")
		}
		if ok1 && ok2 && design > 0 {
			r["health_pct"] = round1(maxc / design * 100)
		}
		if t, ok := num("Temperature"); ok {
			r["temperature_c"] = round1(t / 100)
		}
		v, okv := num("Voltage")
		a, oka := num("Amperage")
		if okv {
			r["voltage_v"] = math.Round(v) / 1000
		}
		if oka {
			r["amps"] = math.Round(a) / 1000
		}
		if okv && oka {
			r["watts"] = round1(v * a / 1e6)
		}
	}
	return []Row{r}, nil
}

// cpu.cores: sysctl hw.perflevel* + top's whole-CPU usage.
func parseCPUCores(in Input) ([]Row, error) {
	sc := kv(in.Out("sysctl"), ":")
	var user, sys any
	var load any
	if all := cpuUsage.FindAllStringSubmatch(in.Out("top"), -1); len(all) > 0 {
		m := all[len(all)-1]
		u, _ := atof(m[1])
		s, _ := atof(m[2])
		user, sys, load = u, s, round1(u+s)
	}
	var rows []Row
	for i := 0; i < 2; i++ {
		p := fmt.Sprintf("hw.perflevel%d.", i)
		cores, ok := atoi(sc[p+"logicalcpu"])
		if !ok {
			continue
		}
		name := sc[p+"name"]
		kind := "P"
		if i == 1 || strings.HasPrefix(strings.ToLower(name), "eff") {
			kind = "E"
		}
		rows = append(rows, Row{"cluster": name, "kind": kind, "cores": cores, "l2_cache": optInt(sc[p+"l2cachesize"]),
			"load_pct": load, "user_pct": user, "sys_pct": sys})
	}
	return rows, nil
}

// thermal.state: pmset -g therm (+ the Intel CPU thermal level when present).
func parseThermalState(in Input) ([]Row, error) {
	rows, err := parsePmsetTherm(Input{Outputs: map[string][]byte{"main": in.Outputs["therm"]}})
	if err != nil || len(rows) == 0 {
		return rows, err
	}
	rows[0]["cpu_thermal_level"] = optInt(strings.TrimSpace(in.Out("sysctl")))
	rows[0]["fan_rpm"] = nil
	return rows, nil
}

var pmPower = regexp.MustCompile(`(?m)^(CPU|GPU|ANE|Combined) Power[^:]*:\s*([0-9.]+)\s*mW`)

// ane.activity: powermetrics --samplers cpu_power,gpu_power,ane_power -n 1.
func parsePowermetricsPower(in Input) ([]Row, error) {
	r := Row{"ane_mw": nil, "gpu_mw": nil, "cpu_mw": nil, "combined_mw": nil, "ane_active": false}
	found := false
	for _, m := range pmPower.FindAllStringSubmatch(in.Main(), -1) {
		f, _ := atof(m[2])
		found = true
		switch m[1] {
		case "CPU":
			r["cpu_mw"] = f
		case "GPU":
			r["gpu_mw"] = f
		case "ANE":
			r["ane_mw"] = f
			r["ane_active"] = f > 0
		case "Combined":
			r["combined_mw"] = f
		}
	}
	if !found {
		return nil, nil
	}
	return []Row{r}, nil
}

var pmPressure = regexp.MustCompile(`Current pressure level:\s*(\w+)`)

// thermal.pressure: powermetrics --samplers thermal -n 1.
func parsePowermetricsThermal(in Input) ([]Row, error) {
	m := pmPressure.FindStringSubmatch(in.Main())
	if m == nil {
		return nil, nil
	}
	levels := map[string]int64{"nominal": 0, "moderate": 1, "heavy": 2, "trapping": 3, "sleeping": 3}
	states := []string{"nominal", "fair", "serious", "critical"}
	r := Row{"pressure": m[1]}
	if l, ok := levels[strings.ToLower(m[1])]; ok {
		r["level"], r["state"] = l, states[l]
	}
	return []Row{r}, nil
}
