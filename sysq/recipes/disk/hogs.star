recipe(
  name   = "disk.hogs",
  doc    = "Largest directories and files under a root.",
  params = {
    "root":  param(Path, default="~"),
    "depth": param(int, default=2, max=3),
    "min":   param(Bytes, default=500MB),
    "limit": param(int, default=15, max=50),
  },
  tags    = ["disk", "cleanup"],
  example = "disk.hogs(root=~/Library, min=1GB)",
)

def run(root, depth, min, limit):
  dirs  = disk.usage(root, depth=depth) | filter size > min and depth > 0 | sort by size desc | limit limit
  files = fs.large(root, min_size=min, limit=limit)
  return { "directories": dirs, "files": files }
