recipe(
  name = "net.headline",
  doc = "Total network throughput (bytes per second in + out) over a one-second sample, loopback excluded.",
  tags = ["network", "headroom"],
)

def run():
  # lo0 carries traffic between local processes (counted once in and once
  # out); it is not network use.
  t = net.throughput() | filter name != "lo0"
  return t | group {} { sum(in_rate) as in_rate, sum(out_rate) as out_rate, sum(in_rate + out_rate) as throughput }
