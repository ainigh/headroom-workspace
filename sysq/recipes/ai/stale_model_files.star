recipe(
  name = "ai.stale_model_files",
  doc = "Model files on disk that are not loaded, largest first, with when their manifest last changed.",
  params = {"dir": param(Path, default = "~/.ollama/models/manifests")},
  tags = ["ai", "disk", "optimize"],
)

def run(dir):
  on_disk = ai.model_files(dir) | group by model { sum(size) as size }
  loaded = ai.ollama() | select model
  idle = on_disk | join loaded on model anti
  return idle | sort by size desc
