recipe(
  name = "net.consumers",
  doc = "Processes by network bytes (in + out) from nettop.",
  params = {"limit": param(int, default = 15, max = 100)},
  tags = ["network", "headroom"],
)

def run(limit):
  return net.by_process | select pid, command, bytes_in, bytes_out, bytes_in + bytes_out as bytes | sort by bytes desc | limit limit
