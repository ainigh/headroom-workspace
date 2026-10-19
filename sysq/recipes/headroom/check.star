recipe(
  name = "headroom.check",
  doc = "Will this fit? Compares a memory (and optional disk) request with what is available now. Available memory counts cached and purgeable pages as reclaimable; compressed memory counts as used.",
  params = {
    "memory": param(Bytes, default = 0),
    "disk": param(Bytes, default = 0),
    "margin": param(float, default = 0.1, min = 0, max = 0.9),
  },
  tags = ["headroom", "api"],
  example = "headroom.check(memory=22GB)",
)

def run(memory, disk, margin):
  m = sys.memory
  avail = num(m.available)
  total = num(m.total)
  pressure = scalar(m.pressure)
  free_disk = num(recipes.disk.headline() | select free)
  need_mem = num(memory)
  need_disk = num(disk)
  mem_ok = need_mem <= avail - total * margin
  disk_ok = need_disk == 0 or need_disk <= free_disk * (1 - margin)
  fits = mem_ok and disk_ok and pressure != "critical"
  verdict = "fits"
  if not fits:
    verdict = "tight" if need_mem <= avail else "does not fit"
  if pressure == "critical":
    verdict = "memory pressure is critical"
  return record(fits = fits, verdict = verdict,
                memory_requested = Bytes(int(need_mem)), memory_available = Bytes(int(avail)),
                memory_after = Bytes(int(avail - need_mem)), pressure = pressure,
                disk_requested = Bytes(int(need_disk)), disk_free = Bytes(int(free_disk)))
