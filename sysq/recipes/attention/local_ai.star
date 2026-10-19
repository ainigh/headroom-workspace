load("lib/attention.star", "finding")

recipe(
  name = "attention.local_ai",
  doc = "Local AI status from memory held by loaded models (bytes).",
  params = {
    "amber": param(float, default = 24000000000.0),
    "red": param(float, default = 40000000000.0),
    "direction": param(str, values = ["up", "down"], default = "up"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try ai.headline()
  value = None
  if count(h) > 0 and "memory_held" in h.columns():
    value = scalar(h.memory_held)
  text = "Local models hold " + humanize(value) if value != None else "No local models"
  return finding("memory_held", value, amber, red, direction, text)
