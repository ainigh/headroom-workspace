recipe(
  name = "mem.headline",
  doc = "Memory available (total minus app, wired and compressed), with pressure.",
  tags = ["memory", "headroom"],
)

def run():
  return sys.memory | select available, total, used, pressure, pressure_pct, swap_used
