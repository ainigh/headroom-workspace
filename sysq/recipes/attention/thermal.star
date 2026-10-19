load("lib/attention.star", "finding")

recipe(
  name = "attention.thermal",
  doc = "Thermal status from the thermal level (0 nominal … 3 critical).",
  params = {
    "amber": param(float, default = 1.0),
    "red": param(float, default = 2.0),
    "direction": param(str, values = ["up", "down"], default = "up"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try thermal.headline()
  value = None
  if count(h) > 0 and "level" in h.columns():
    value = scalar(h.level)
  text = "Thermal level " + str(value) if value != None else "Thermal state unknown"
  return finding("level", value, amber, red, direction, text)
