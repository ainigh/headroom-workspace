recipe(
  name = "gpu.headline",
  doc = "GPU utilization and memory in use (IOAccelerator statistics).",
  tags = ["gpu", "headroom"],
)

def run():
  return gpu.stats | select utilization, in_use_mem, alloc_mem
