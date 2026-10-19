recipe(
  name = "software.outdated",
  doc = "macOS version and update history, and Homebrew formulas with several versions installed. Whether newer releases exist needs a network check, which sysq does not do.",
  tags = ["software"],
)

def run():
  macos = sys.info | select os, os_version, build
  history = try sys.updates | limit 15
  multi = try pkg.brew | group by name { count() as versions, list(version) as installed } | filter versions > 1
  return {"macos": macos, "update_history": history, "brew_multiple_versions": multi}
