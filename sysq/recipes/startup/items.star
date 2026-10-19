recipe(
  name = "startup.items",
  doc = "Launch agents and daemons (user and system plists) and the launchd jobs loaded in this session.",
  tags = ["startup", "security"],
)

DIRS = ["~/Library/LaunchAgents", "/Library/LaunchAgents", "/Library/LaunchDaemons"]

def run():
  plists = None
  for d in DIRS:
    l = try fs.list(Path(d), glob = "*.plist")
    if count(l) > 0:
      l = l | select name, path, modified
      plists = l if plists == None else plists | union l
  jobs = svc.launchd | filter not label startswith "com.apple."
  return {"plists": plists or table([]), "jobs": jobs}
