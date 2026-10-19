recipe(
  name = "net.who_listens",
  doc = "Listening TCP ports with the owning process, its executable and where it came from (Homebrew, app bundle, system).",
  tags = ["network", "security"],
)

def origin(exe):
  if exe.startswith("/opt/homebrew") or exe.startswith("/usr/local/Cellar"):
    return "homebrew"
  if "/Applications/" in exe:
    return "app"
  if exe.startswith("/System") or exe.startswith("/usr/") or exe.startswith("/sbin"):
    return "system"
  return "other"

def run():
  l = net.listeners | join (proc.list | select pid, name, exe, user) on pid left
  rows = []
  for r in l:
    rows.append({"port": r.port, "address": r.address, "pid": r.pid, "command": r.command, "exe": r.exe or "", "origin": origin(str(r.exe or "")), "user": r.user or ""})
  if len(rows) == 0:
    return l
  return table(rows) | sort by port
