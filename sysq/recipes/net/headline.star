recipe(
  name = "net.headline",
  doc = "Total network throughput (bytes per second in + out) over a one-second sample.",
  tags = ["network", "headroom"],
)

def run():
  t = net.throughput()
  return t | group {} { sum(in_rate) as in_rate, sum(out_rate) as out_rate, sum(in_rate + out_rate) as throughput }
