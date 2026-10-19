recipe(
  name = "ai.idle_models",
  doc = "Loaded models that are idle (Ollama keeps them until their unload time): candidates for ai.ollama_unload.",
  tags = ["ai", "optimize"],
)

def run():
  loaded = ai.ollama()
  runners = proc.list | filter name == "ollama" and command contains "runner" and cpu < 1
  if count(runners) == 0:
    return loaded | limit 0
  return loaded
