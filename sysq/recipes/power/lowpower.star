recipe(
  name = "power.lowpower",
  doc = "Whether Low Power Mode is on and the current power source.",
  tags = ["battery", "power"],
)

def run():
  return sys.power | select low_power_mode, source
