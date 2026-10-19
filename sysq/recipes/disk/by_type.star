recipe(
  name = "disk.by_type",
  doc = "Space used by file type (extension) among large files under a root.",
  params = {"root": param(Path, default = "~"), "min": param(Bytes, default = 50MB)},
  tags = ["disk", "explorer"],
)

def run(root, min):
  return fs.large(root, min_size = min, limit = 200) | group by ext(path) as type { count() as files, sum(size) as size } | sort by size desc | limit 25
