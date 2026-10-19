load("lib/attention.star", "finding")

recipe(
  name = "attention.network",
  doc = "Network status from total throughput (bytes per second).",
  params = {
    "amber": param(float, default = 50000000.0),
    "red": param(float, default = 100000000.0),
    "direction": param(str, values = ["up", "down"], default = "up"),
  },
  tags = ["attention", "headroom"],
  severity = "medium",
  when = "status != \"ok\" and status != \"unknown\"",
  message = "{message}",
  schedule = "1m",
)

def run(amber, red, direction):
  h = try net.headline()
  value = None
  if count(h) > 0 and "throughput" in h.columns():
    value = scalar(h.throughput)
  text = "Throughput " + humanize(Bytes(int(num(value)))) + "/s" if value != None else "Throughput unknown"
  return finding("throughput", value, amber, red, direction, text)
