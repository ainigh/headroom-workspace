recipe(
  name = "startup.drift",
  doc = "Launch agents/daemons added, removed or changed since the baseline (sysq baseline capture).",
  tags = ["startup", "security", "changes"],
  severity = "high",
  when = "change == \"added\"",
  message = "New startup item {name}",
  baseline = "startup",
  schedule = "1h",
)

DIRS = ["~/Library/LaunchAgents", "/Library/LaunchAgents", "/Library/LaunchDaemons"]

def run():
  out = None
  for d in DIRS:
    l = try fs.list(Path(d), glob = "*.plist")
    if count(l) > 0:
      l = l | select name, path, size, modified
      out = l if out == None else out | union l
  return out or table([{"name": "", "path": "", "size": Bytes(0), "modified": now()}]) | limit 0
