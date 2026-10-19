recipe(
  name = "software.recent_installs",
  doc = "Apps and Homebrew packages added or changed recently.",
  params = {"since": param(Duration, default = 14d)},
  tags = ["software", "changes"],
)

def run(since):
  apps = try fs.list(/Applications) | filter kind == "dir" and name endswith ".app" and modified > ago(since) | select name, modified | sort by modified desc
  brew = try fs.list(/opt/homebrew/Cellar) | filter kind == "dir" and modified > ago(since) | select name, modified | sort by modified desc
  return {"apps": apps, "brew": brew}
