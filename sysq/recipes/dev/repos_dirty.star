recipe(
  name = "dev.repos_dirty",
  doc = "Git repositories under a root with uncommitted changes or unpushed commits.",
  params = {"root": param(Path, default = "~/Developer"), "depth": param(int, default = 3, max = 5), "limit": param(int, default = 30, max = 60)},
  tags = ["dev", "git"],
)

def run(root, depth, limit):
  repos = fs.find_dirs(root, name = ".git", depth = depth) | limit limit
  rows = []
  for r in repos:
    s = try git.status(r.path.parent)
    if count(s) == 0:
      continue
    if num(s.changed) > 0 or num(s.untracked) > 0 or num(s.ahead) > 0:
      rows.append({"repo": r.path.parent, "branch": s.branch[0], "changed": s.changed[0], "untracked": s.untracked[0], "ahead": s.ahead[0]})
  if len(rows) == 0:
    return table([{"repo": Path("/"), "branch": "", "changed": 0, "untracked": 0, "ahead": 0}]) | limit 0
  return table(rows) | sort by changed desc
