recipe(
  name = "health.overview",
  doc = "One table each for cpu, memory, disk, thermal, battery and uptime, plus a one-line verdict.",
  tags = ["health", "summary"],
  example = "health.overview()",
)

def run():
  mem = sys.memory | select total, used, available, pressure, pressure_pct, swap_used
  cpu = sys.cpu | select brand, cores, load_pct, load1
  boot = disk.volumes | filter mount == "/" | select name, size, free, used_pct
  thermal = sys.thermal | select state, level, speed_limit
  power = try sys.power
  uptime = try sys.uptime
  problems = []
  if scalar(mem.pressure) in ["warn", "critical"]:
    problems.append("memory " + scalar(mem.pressure))
  if num(cpu.load_pct) >= 80:
    problems.append("cpu busy")
  if count(boot) > 0 and num(boot.used_pct) >= 90:
    problems.append("disk nearly full")
  if num(thermal.level) >= 2:
    problems.append("running hot")
  verdict = "Healthy" if len(problems) == 0 else "Attention — " + ", ".join(problems)
  return {
    "verdict": record(verdict = verdict, problems = len(problems)),
    "cpu": cpu, "memory": mem, "disk": boot, "thermal": thermal,
    "battery": power, "uptime": uptime,
  }
