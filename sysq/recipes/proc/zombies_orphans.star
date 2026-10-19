recipe(
  name = "proc.zombies_orphans",
  doc = "Zombie processes (exited, not reaped) and user processes re-parented to launchd that are not apps or agents.",
  tags = ["process", "repair"],
)

def run():
  procs = proc.list
  zombies = procs | filter state contains "Z"
  orphans = procs | filter ppid == 1 and user != "root" and not exe contains ".app/" and not exe startswith "/System" and not exe startswith "/usr/libexec" and not exe startswith "/usr/sbin"
  return {"zombies": zombies, "orphans": orphans | select pid, name, exe, rss, started}
