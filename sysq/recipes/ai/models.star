recipe(
  name = "ai.models",
  doc = "Ollama models on disk with their total size.",
  tags = ["ai", "disk"],
)

def run():
  return ai.model_files | group by model { sum(size) as size, count() as layers } | sort by size desc
