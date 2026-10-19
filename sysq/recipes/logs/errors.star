recipe(
  name = "logs.errors",
  doc = "Unified log errors and faults grouped by process.",
  params = {"since": param(Duration, default = 1h, max = 86400), "process": param(str, default = "", max_len = 100)},
  tags = ["logs", "repair"],
)

def run(since, process):
  l = log.system(since = since, limit = 5000) | filter level == "error" or level == "fault"
  if process != "":
    l = l | filter process == process
  return l | group by process { count() as errors, max(ts) as last, first(message) as example } | sort by errors desc | limit 25
