recipe(
  name = "ai.gpu_holders",
  doc = "Processes likely holding large GPU (unified) memory: local model runners and the biggest GPU clients. Memory is the process's resident size (an estimate).",
  params = {"min": param(Bytes, default = 500MB)},
  tags = ["gpu", "ai", "headroom"],
)

RUNNERS = ["ollama", "llama", "mlx", "lms", "LM Studio", "llama-server", "koboldcpp", "ComfyUI", "WindowServer"]

def run(min):
  procs = proc.list | filter rss >= min
  rows = []
  for p in procs:
    hit = False
    for r in RUNNERS:
      if r in p.name or r in p.command:
        hit = True
    if hit:
      rows.append({"pid": p.pid, "name": p.name, "gpu_mem": p.rss, "cpu": p.cpu})
  if len(rows) == 0:
    return table([{"pid": 0, "name": "", "gpu_mem": Bytes(0), "cpu": 0.0}]) | limit 0
  return table(rows) | sort by gpu_mem desc
