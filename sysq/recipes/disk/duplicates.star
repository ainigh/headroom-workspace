recipe(
  name = "disk.duplicates",
  doc = "Groups of identical large files under a root (same size, then same SHA-256), capped.",
  params = {
    "root": param(Path, default = "~"),
    "min": param(Bytes, default = 100MB),
    "limit": param(int, default = 30, max = 60),
  },
  tags = ["disk", "cleanup", "optimize"],
)

def run(root, min, limit):
  files = fs.large(root, min_size = min, limit = 200)
  same = files | group by size { count() as n } | filter n > 1
  candidates = files | join same on size | sort by size desc | limit limit
  hashed = []
  for f in candidates:
    h = try fs.hash(f.path)
    if count(h) > 0:
      hashed.append({"path": f.path, "size": f.size, "sha256": h.sha256[0]})
  if len(hashed) == 0:
    return table([]) | limit 0
  dupes = table(hashed) | group by sha256 { count() as copies, max(size) as size, list(path) as paths } | filter copies > 1
  return dupes | select sha256, copies, size, size * (copies - 1) as reclaimable, paths | sort by reclaimable desc
