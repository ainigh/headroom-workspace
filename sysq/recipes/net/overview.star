recipe(
  name = "net.overview",
  doc = "Interfaces, Wi-Fi, DNS and routes in one record.",
  tags = ["network"],
)

def run():
  return {
    "interfaces": net.interfaces | filter up and (ipv4 != "" or status == "active"),
    "wifi": try net.wifi,
    "dns": try net.dns,
    "routes": try net.routes | limit 20,
  }
