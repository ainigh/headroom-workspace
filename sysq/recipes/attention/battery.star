load("lib/attention.star", "finding")

recipe(
  name = "attention.battery",
  doc = "Battery status from the charge level.",
  params = {
    "amber": param(float, default = 30.0),
    "red": param(float, default = 10.0),
    "direction": param(str, values = ["up", "down"], default = "down"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try battery.headline()
  value = None
  if count(h) > 0 and "level" in h.columns():
    value = scalar(h.level)
  text = "Battery " + str(value) + "%" if value != None else "No battery"
  return finding("level", value, amber, red, direction, text)
