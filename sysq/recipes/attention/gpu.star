load("lib/attention.star", "finding")

recipe(
  name = "attention.gpu",
  doc = "GPU status from utilization.",
  params = {
    "amber": param(float, default = 80.0),
    "red": param(float, default = 95.0),
    "direction": param(str, values = ["up", "down"], default = "up"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try gpu.stats
  value = None
  if count(h) > 0 and "utilization" in h.columns():
    value = scalar(h.utilization)
  text = "GPU utilization " + str(value) + "%" if value != None else "GPU utilization unknown"
  return finding("utilization", value, amber, red, direction, text)
