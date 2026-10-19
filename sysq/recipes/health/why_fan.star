recipe(
  name = "health.why_fan",
  doc = "Why the fan is loud: CPU hogs, thermal level and whether the Mac is on battery.",
  tags = ["health", "thermal", "cpu"],
)

def run():
  hogs = proc.top(by = "cpu", n = 10) | filter cpu > 20
  hot = sys.thermal
  power = try sys.power
  return {"hogs": hogs, "thermal": hot, "power": power}
