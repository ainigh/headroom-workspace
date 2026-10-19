load("lib/attention.star", "finding")

recipe(
  name = "attention.memory",
  doc = "Memory status from the pressure percentage (thresholds come from Headroom's thresholds.yaml).",
  params = {
    "amber": param(float, default = 60.0),
    "red": param(float, default = 85.0),
    "direction": param(str, values = ["up", "down"], default = "up"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try sys.memory
  value = None
  if count(h) > 0 and "pressure_pct" in h.columns():
    value = scalar(h.pressure_pct)
  text = "Memory pressure " + str(value) + "%" if value != None else "Memory pressure unknown"
  return finding("pressure_pct", value, amber, red, direction, text)
