recipe(
  name = "disk.health",
  doc = "Volume fullness, purgeable space and recent APFS/disk errors in the log.",
  tags = ["disk", "repair"],
)

def run():
  vols = disk.volumes | filter not internal | select name, mount, free, used_pct
  errs = try log.system(since = 24h) | filter subsystem contains "apfs" or process == "fsck_apfs" or message contains "I/O error" | filter level in ["error", "fault"] | limit 20
  purge = try disk.purgeable
  return {"volumes": vols, "purgeable": purge, "errors": errs}
