recipe(
  name = "changes.since",
  doc = "What changed since a named snapshot set: startup items, listeners and Homebrew packages (take one with changes.capture).",
  params = {"label": param(str, default = "changes", pattern = "^[A-Za-z0-9_.-]{1,40}$")},
  tags = ["changes", "security"],
)

def run(label):
  items = startup.drift() | select name, path
  listening = net.listeners | select port, command | distinct port, command
  formulas = try pkg.brew | select name, version
  return {
    "startup": items | diff snapshot(label + ".startup") on name,
    "listeners": listening | diff snapshot(label + ".listeners") on port,
    "brew": formulas | diff snapshot(label + ".brew") on name,
  }
