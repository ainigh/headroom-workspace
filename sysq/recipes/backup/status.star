recipe(
  name = "backup.status",
  doc = "Time Machine: whether a backup is running, the last backup and hours since.",
  tags = ["backup", "repair"],
  severity = "medium",
  when = "hours_since_last_backup > 48",
  message = "Last Time Machine backup was {hours_since_last_backup} hours ago",
  schedule = "1h",
)

def run():
  return tm.status
