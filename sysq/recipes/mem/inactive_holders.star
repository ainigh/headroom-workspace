recipe(
  name = "mem.inactive_holders",
  doc = "Apps holding a lot of memory while idle (near-zero CPU): candidates to quit.",
  params = {"min": param(Bytes, default = 300MB), "limit": param(int, default = 10, max = 50)},
  tags = ["memory", "optimize"],
)

def run(min, limit):
  return proc.list | filter rss >= min and cpu < 1 and user != "root" | select pid, name, rss as memory, cpu, started, now() - started as uptime | sort by memory desc | limit limit
