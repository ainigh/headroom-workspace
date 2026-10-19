package parsers

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

func init() {
	Register("ps_list", parsePsList)
	Register("top_table", parseTopTable)
	Register("launchctl_list", parseLaunchctlList)
	Register("log_ndjson", parseLogNDJSON)
}

// proc.list: ps with lstart (5 fixed tokens) + a second ps for the
// executable path (comm can contain spaces, so it gets its own call).
func parsePsList(in Input) ([]Row, error) {
	exe := map[int64]string{}
	for _, l := range lines(in.Out("comm")) {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		pid, ok := atoi(f[0])
		if !ok {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), f[0]))
		exe[pid] = rest
	}
	var rows []Row
	for _, l := range lines(in.Out("ps")) {
		f := strings.Fields(l)
		if len(f) < 13 {
			continue
		}
		pid, ok := atoi(f[0])
		if !ok {
			continue
		}
		ppid, _ := atoi(f[1])
		cpu, _ := atof(f[4])
		memPct, _ := atof(f[5])
		rss, _ := atoi(f[6])
		var started any
		if t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[7:12], " "), loc(in)); err == nil {
			started = t
		}
		cmd := strings.Join(f[12:], " ")
		path := exe[pid]
		name := basename(path)
		if name == "" {
			name = basename(strings.Fields(cmd)[0])
		}
		rows = append(rows, Row{
			"pid": pid, "ppid": ppid, "user": f[2], "state": f[3], "cpu": cpu, "mem_pct": memPct,
			"rss": rss * 1024, "started": started, "name": name, "exe": path, "command": cmd,
		})
	}
	return rows, nil
}

var topHeader = regexp.MustCompile(`^\s*PID\s+COMMAND`)

// proc.top: the last sample of `top -l 2 ... -stats pid,command,cpu,mem,power`.
// COMMAND is truncated by top and may contain spaces: pid is the first
// field, cpu/mem/power the last three.
func parseTopTable(in Input) ([]Row, error) {
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
		if len(f) < 5 {
			continue
		}
		pid, ok := atoi(f[0])
		if !ok {
			continue
		}
		n := len(f)
		cpu, ok1 := atof(f[n-3])
		mem, ok2 := suffixBytes(f[n-2])
		power, ok3 := atof(f[n-1])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		rows = append(rows, Row{
			"pid": pid, "command": strings.Join(f[1:n-3], " "),
			"cpu": cpu, "mem": mem, "power": power,
		})
	}
	return rows, nil
}

// svc.launchd: launchctl list (PID, Status, Label).
func parseLaunchctlList(in Input) ([]Row, error) {
	var rows []Row
	for _, l := range lines(in.Main()) {
		f := strings.Split(l, "\t")
		if len(f) != 3 || f[0] == "PID" {
			continue
		}
		pid := optInt(f[0])
		rows = append(rows, Row{
			"label": f[2], "pid": pid, "last_exit": optInt(f[1]), "running": pid != nil,
		})
	}
	return rows, nil
}

// log.system: log show --style ndjson.
func parseLogNDJSON(in Input) ([]Row, error) {
	limit := int64(500)
	if n, ok := in.Params["limit"].(int64); ok && n > 0 {
		limit = n
	}
	var rows []Row
	for _, l := range lines(in.Main()) {
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var e struct {
			Timestamp   string `json:"timestamp"`
			Message     string `json:"eventMessage"`
			EventType   string `json:"eventType"`
			Subsystem   string `json:"subsystem"`
			Category    string `json:"category"`
			ProcessPath string `json:"processImagePath"`
			PID         int64  `json:"processID"`
			MessageType string `json:"messageType"`
		}
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		if e.EventType != "" && e.EventType != "logEvent" {
			continue
		}
		var ts any
		if t, err := time.Parse("2006-01-02 15:04:05.000000-0700", e.Timestamp); err == nil {
			ts = t
		}
		rows = append(rows, Row{
			"ts": ts, "process": basename(e.ProcessPath), "pid": e.PID,
			"subsystem": e.Subsystem, "category": e.Category,
			"level": strings.ToLower(e.MessageType), "message": e.Message,
		})
	}
	// Keep the most recent entries.
	if int64(len(rows)) > limit {
		rows = rows[int64(len(rows))-limit:]
	}
	return rows, nil
}
