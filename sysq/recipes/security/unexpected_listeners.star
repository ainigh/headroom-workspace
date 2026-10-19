recipe(
  name = "security.unexpected_listeners",
  doc = "TCP listeners not present in the baseline (sysq baseline capture), or on all interfaces.",
  tags = ["security", "network"],
  severity = "high",
  when = "change == \"added\"",
  message = "New listener {command} on port {port}",
  baseline = "listeners",
  schedule = "15m",
)

def run():
  return net.listeners | select port, command, address | distinct port, command
