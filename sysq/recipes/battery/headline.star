recipe(
  name = "battery.headline",
  doc = "Battery level, power source and time remaining.",
  tags = ["battery", "headroom"],
)

def run():
  return sys.power | select percent as level, source, state, remaining, low_power_mode
