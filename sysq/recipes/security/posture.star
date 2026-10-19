recipe(
  name = "security.posture",
  doc = "SIP, FileVault, Gatekeeper and the application firewall in one table.",
  tags = ["security"],
)

def run():
  s = sec.status
  fw = try net.firewall
  return {"system": s, "firewall": fw}
