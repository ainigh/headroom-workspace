recipe(
  name = "thermal.headline",
  doc = "Thermal state (nominal, fair, serious, critical) and its level 0–3; uses powermetrics' pressure level when a sudo rule allows it.",
  tags = ["thermal", "headroom"],
)

def run():
  t = sys.thermal
  p = try thermal.pressure
  if count(p) > 0 and p.meta()["unavailable"] == "":
    return p | select state, level
  return t | select state, level, speed_limit
