recipe(
  name = "ai.ollama",
  doc = "Models loaded in Ollama with memory held and when they unload. Uses `ollama ps` when that network primitive is enabled; otherwise reads the runner processes.",
  tags = ["ai", "headroom"],
)

def run():
  ps = try ai.ollama_ps
  if count(ps) > 0:
    return ps | select model, size as memory, processor, until
  runners = proc.list | filter name == "ollama" and command contains "runner"
  files = try ai.model_files
  rows = []
  for r in runners:
    model = ""
    for f in files:
      if f.digest.replace("sha256:", "sha256-") in r.command:
        model = f.model
    rows.append({"model": model or "pid " + str(r.pid), "memory": r.rss, "processor": "", "until": ""})
  if len(rows) == 0:
    return table([{"model": "", "memory": Bytes(0), "processor": "", "until": ""}]) | limit 0
  return table(rows) | sort by memory desc
