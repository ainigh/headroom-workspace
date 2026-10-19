load("lib/attention.star", "finding")

recipe(
  name = "attention.cpu",
  doc = "CPU status from overall load.",
  params = {
    "amber": param(float, default = 70.0),
    "red": param(float, default = 90.0),
    "direction": param(str, values = ["up", "down"], default = "up"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try sys.cpu
  value = None
  if count(h) > 0 and "load_pct" in h.columns():
    value = scalar(h.load_pct)
  text = "CPU load " + str(value) + "%" if value != None else "CPU load unknown"
  return finding("load_pct", value, amber, red, direction, text)
