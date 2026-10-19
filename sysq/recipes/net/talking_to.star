recipe(
  name = "net.talking_to",
  doc = "Connections to a host or port, with the owning processes.",
  params = {"to_host": param(str, default = ""), "to_port": param(int, default = 0, min = 0, max = 65535)},
  tags = ["network"],
  example = "net.talking_to(to_port=443)",
)

def run(to_host, to_port):
  c = net.connections
  if to_host != "":
    c = c | filter remote_address contains to_host
  if to_port > 0:
    c = c | filter remote_port == to_port
  return c | join (proc.list | select pid, name, exe) on pid left
