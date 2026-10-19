recipe(
  name = "net.throughput",
  doc = "Bytes per second in and out per interface, from two netstat samples one second apart.",
  params = {"every": param(Duration, default = 1s)},
  tags = ["network", "headroom"],
)

def run(every):
  s = watch(net.netstat, every = every, for_ = every)
  rates = s | window rate(ibytes) over (partition by name order by t) as in_rate, rate(obytes) over (partition by name order by t) as out_rate
  return rates | filter in_rate != null | select name, in_rate, out_rate, ibytes, obytes | sort by in_rate desc
