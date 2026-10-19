recipe(
  name = "net.destinations",
  doc = "Remote hosts this Mac is talking to, with connection counts and the processes involved.",
  tags = ["network", "deep_dive"],
)

def run():
  return net.connections | filter remote_address != "" and remote_address != null | group by remote_address, remote_port { count() as conns, count_distinct(pid) as processes, list(command) as commands } | sort by conns desc | limit 30
