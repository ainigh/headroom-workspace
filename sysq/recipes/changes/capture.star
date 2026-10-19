recipe(
  name = "changes.capture",
  doc = "Record the current startup items, listeners and Homebrew packages as a named snapshot set for changes.since.",
  params = {"label": param(str, default = "changes", pattern = "^[A-Za-z0-9_.-]{1,40}$")},
  tags = ["changes"],
)

def run(label):
  s = snapshot(label + ".startup", startup.drift() | select name, path)
  l = snapshot(label + ".listeners", net.listeners | select port, command | distinct port, command)
  b = snapshot(label + ".brew", try pkg.brew | select name, version)
  return record(label = label, startup = count(s), listeners = count(l), brew = count(b))
