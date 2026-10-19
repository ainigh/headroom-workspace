recipe(
  name = "disk.headline",
  doc = "Free space on the boot volume.",
  tags = ["disk", "headroom"],
)

def run():
  return disk.volumes | filter mount == "/" | select name, free, size, used, used_pct
