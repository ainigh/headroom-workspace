recipe(
  name = "dev.toolchain",
  doc = "Versions of the developer tools installed (git, go, node, python, rust, swift, docker).",
  tags = ["dev"],
)

def run():
  return dev.tools
