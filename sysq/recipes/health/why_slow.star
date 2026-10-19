recipe(
  name = "health.why_slow",
  doc = "Top CPU and memory consumers with their connection counts, full volumes, swap and thermal state (the design §16 script).",
  tags = ["health", "cpu", "memory"],
)

def run():
  cpu = proc.top(by = "cpu", n = 10)
  mem = proc.top(by = "mem", n = 10)
  io = disk.volumes | filter used_pct > 85 and not internal
  swap = sys.memory | select swap_used, pressure
  hot = sys.thermal
  conns = net.connections | group by pid { count() as conns }
  culprits = cpu | union mem | distinct pid | join conns on pid left
  verdict = "No single culprit"
  if any(cpu, expr("cpu > 80")) and num(hot.level) >= 2:
    verdict = "Likely thermal throttling from a CPU hog"
  elif scalar(swap.pressure) != "normal":
    verdict = "Memory pressure is " + scalar(swap.pressure)
  elif any(cpu, expr("cpu > 80")):
    verdict = "A CPU hog: " + str(scalar(cpu.command[0]))
  return {
    "verdict": record(verdict = verdict),
    "culprits": culprits | sort by cpu desc | limit 8,
    "memory": swap, "thermal": hot, "full_volumes": io,
  }
