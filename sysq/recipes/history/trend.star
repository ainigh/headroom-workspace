recipe(
  name = "history.trend",
  doc = "Min, max, average and latest of a sampled recipe column over a range (needs sysqd's history).",
  params = {
    "recipe": param(str, default = "cpu.headline", pattern = "^[a-z][a-z0-9_.]*$"),
    "column": param(str, default = "load_pct", pattern = "^[a-z_][a-z0-9_]*$"),
    "range": param(Duration, default = 1h),
  },
  tags = ["history", "headroom"],
)

def run(recipe, column, range):
  h = history(recipe, range = range, column = column)
  if count(h) == 0:
    return h
  return h | group {} { min(value) as min, max(value) as max, avg(value) as avg, last(value) as last, count() as samples }
