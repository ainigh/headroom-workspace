recipe(
  name = "mem.health",
  doc = "Signs of memory trouble: pressure, swap in use, pageouts, and recent memorystatus log events.",
  tags = ["memory", "repair"],
)

def run():
  m = sys.memory | select pressure, pressure_pct, swap_used, pageouts, swapouts, compressed
  events = try log.system(since = 2h) | filter category == "memorystatus" or message contains "memory pressure" | limit 20
  return {"memory": m, "events": events}
