load("lib/attention.star", "finding")

recipe(
  name = "attention.storage",
  doc = "Storage status from free space on the boot volume (bytes).",
  params = {
    "amber": param(float, default = 50000000000.0),
    "red": param(float, default = 20000000000.0),
    "direction": param(str, values = ["up", "down"], default = "down"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try disk.headline()
  value = None
  if count(h) > 0 and "free" in h.columns():
    value = scalar(h.free)
  text = "Boot volume free " + humanize(value) if value != None else "Free space unknown"
  return finding("free", value, amber, red, direction, text)
