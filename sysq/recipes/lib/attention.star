# Helpers for attention.<group> recipes (loaded with load("lib/attention.star", ...)).

def level(value, amber, red, direction):
  """ok | amber | red for a value against thresholds; higher is worse unless direction is "down"."""
  if value == None:
    return "unknown"
  v = num(value)
  if direction == "down":
    if v <= red:
      return "red"
    if v <= amber:
      return "amber"
    return "ok"
  if v >= red:
    return "red"
  if v >= amber:
    return "amber"
  return "ok"

def finding(metric, value, amber, red, direction, text):
  return record(status = level(value, amber, red, direction), metric = metric, value = value,
                amber = amber, red = red, direction = direction, message = text)
