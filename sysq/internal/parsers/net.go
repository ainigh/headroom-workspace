package parsers

import (
	"regexp"
	"strings"
)

func init() {
	Register("lsof_net", parseLsofNet)
	Register("ifconfig", parseIfconfig)
}

// net.listeners / net.connections: lsof -F output (one field per line).
func parseLsofNet(in Input) ([]Row, error) {
	var rows []Row
	var pid int64
	var cmd, uid string
	var cur Row
	flush := func() {
		if cur != nil {
			rows = append(rows, cur)
			cur = nil
		}
	}
	for _, l := range lines(in.Main()) {
		if len(l) < 1 {
			continue
		}
		tag, val := l[0], l[1:]
		switch tag {
		case 'p':
			flush()
			pid, _ = atoi(val)
			cmd, uid = "", ""
		case 'c':
			cmd = val
		case 'u':
			uid = val
		case 'f':
			flush()
			cur = Row{"pid": pid, "command": cmd, "uid": optInt(uid)}
		case 't':
			if cur != nil {
				cur["family"] = val
			}
		case 'P':
			if cur != nil {
				cur["proto"] = val
			}
		case 'n':
			if cur == nil {
				continue
			}
			local, remote, _ := strings.Cut(val, "->")
			addr, port := splitHostPort(local)
			cur["address"], cur["port"] = addr, optInt(port)
			if remote != "" {
				raddr, rport := splitHostPort(remote)
				cur["remote_address"], cur["remote_port"] = raddr, optInt(rport)
			}
		case 'T':
			if cur != nil && strings.HasPrefix(val, "ST=") {
				cur["state"] = strings.TrimPrefix(val, "ST=")
			}
		}
	}
	flush()
	return rows, nil
}

func splitHostPort(s string) (string, string) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, ""
	}
	host := strings.TrimSuffix(strings.TrimPrefix(s[:i], "["), "]")
	return host, s[i+1:]
}

var (
	ifHead = regexp.MustCompile(`^([a-z0-9]+): flags=\d+<([^>]*)> mtu (\d+)`)
	hwPort = regexp.MustCompile(`(?m)^Hardware Port: (.+)\nDevice: (\S+)`)
)

// net.interfaces: ifconfig -a + networksetup -listallhardwareports.
func parseIfconfig(in Input) ([]Row, error) {
	ports := map[string]string{}
	for _, m := range hwPort.FindAllStringSubmatch(in.Out("networksetup"), -1) {
		ports[m[2]] = strings.TrimSpace(m[1])
	}
	var rows []Row
	var cur Row
	for _, l := range strings.Split(in.Out("ifconfig"), "\n") {
		if m := ifHead.FindStringSubmatch(l); m != nil {
			if cur != nil {
				rows = append(rows, cur)
			}
			flags := strings.Split(m[2], ",")
			up := false
			for _, f := range flags {
				if f == "UP" {
					up = true
				}
			}
			cur = Row{"name": m[1], "flags": m[2], "mtu": optInt(m[3]), "up": up,
				"hardware_port": ports[m[1]], "status": "", "mac": "", "ipv4": "", "ipv6": ""}
			continue
		}
		if cur == nil {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "ether":
			cur["mac"] = f[1]
		case "inet":
			if cur["ipv4"] == "" {
				cur["ipv4"] = f[1]
			}
		case "inet6":
			addr := f[1]
			if i := strings.IndexByte(addr, '%'); i >= 0 {
				addr = addr[:i]
			}
			if cur["ipv6"] == "" || strings.HasPrefix(cur["ipv6"].(string), "fe80") {
				cur["ipv6"] = addr
			}
		case "status:":
			cur["status"] = f[1]
		}
	}
	if cur != nil {
		rows = append(rows, cur)
	}
	return rows, nil
}
