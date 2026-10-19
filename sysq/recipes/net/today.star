recipe(
  name = "net.today",
  doc = "Bytes received and sent per interface since boot (netstat counters).",
  tags = ["network"],
)

def run():
  return net.netstat | filter ibytes > 0 or obytes > 0 | select name, ibytes as received, obytes as sent | sort by received desc
