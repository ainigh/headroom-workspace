recipe(
  name = "disk.caches",
  doc = "Sizes of known cache directories (Xcode DerivedData, simulators, Homebrew, npm, pip, Docker, app caches).",
  tags = ["disk", "cleanup", "optimize"],
)

KNOWN = [
  ("Xcode DerivedData", "~/Library/Developer/Xcode/DerivedData"),
  ("Xcode archives", "~/Library/Developer/Xcode/Archives"),
  ("iOS simulators", "~/Library/Developer/CoreSimulator/Devices"),
  ("Homebrew", "~/Library/Caches/Homebrew"),
  ("npm", "~/.npm/_cacache"),
  ("pip", "~/Library/Caches/pip"),
  ("Yarn", "~/Library/Caches/Yarn"),
  ("Go build", "~/Library/Caches/go-build"),
  ("Docker", "~/Library/Containers/com.docker.docker/Data"),
  ("Hugging Face", "~/.cache/huggingface"),
]

def run():
  rows = []
  for kind, p in KNOWN:
    u = try disk.usage(Path(p), depth = 0)
    if count(u) > 0:
      rows.append({"kind": kind, "path": Path(p), "size": u.size[0]})
  apps = try disk.usage(~/Library/Caches, depth = 1) | filter depth == 1 and size > 100MB | sort by size desc | limit 15
  known = table(rows) | sort by size desc if len(rows) > 0 else table(rows)
  return {"known": known, "app_caches": apps}
