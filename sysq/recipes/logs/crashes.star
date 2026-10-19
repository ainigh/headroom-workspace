recipe(
  name = "logs.crashes",
  doc = "Crash reports in the recent past, grouped by process.",
  params = {"since": param(Duration, default = 7d)},
  tags = ["logs", "repair"],
)

def run(since):
  c = log.crashes(since = since)
  return c | group by process { count() as crashes, max(ts) as last } | sort by crashes desc
