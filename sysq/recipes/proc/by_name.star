recipe(
  name = "proc.by_name",
  doc = "Processes whose name or command contains a string, with their open files and sockets.",
  params = {"cmd": param(str, required = True, max_len = 100), "limit": param(int, default = 5, max = 10)},
  tags = ["process"],
  example = "proc.by_name(cmd=\"node\")",
)

def run(cmd, limit):
  matches = proc.list | filter name contains cmd or command contains cmd | sort by rss desc | limit limit
  details = []
  for p in matches:
    d = try proc.info(pid = p.pid)
    if count(d) > 0:
      details.append(d)
  out = matches
  if len(details) > 0:
    merged = details[0]
    for d in details[1:]:
      merged = merged | union d
    out = merged
  return out
