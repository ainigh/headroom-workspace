recipe(
  name = "disk.recent_big",
  doc = "Large files modified recently (downloads, caches, exports).",
  params = {
    "root": param(Path, default = "~"),
    "since": param(Duration, default = 7d),
    "min": param(Bytes, default = 200MB),
    "limit": param(int, default = 20, max = 100),
  },
  tags = ["disk", "cleanup"],
  example = "disk.recent_big(since=2d, min=1GB)",
)

def run(root, since, min, limit):
  return fs.recent(root, since = since, min_size = min) | sort by size desc | limit limit
