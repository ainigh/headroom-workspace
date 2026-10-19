recipe(
  name = "cpu.headline",
  doc = "Overall CPU load (user + system) and load average.",
  tags = ["cpu", "headroom"],
)

def run():
  return sys.cpu | select load_pct, user_pct, sys_pct, load1, cores
