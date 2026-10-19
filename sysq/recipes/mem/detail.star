recipe(
  name = "mem.detail",
  doc = "Memory breakdown: used, app, wired, compressed, cached, swap and pressure.",
  tags = ["memory", "headroom"],
)

def run():
  return sys.memory | select used, available, app, wired, compressed, cached, purgeable, swap_used, swap_total, pressure, pressure_pct
