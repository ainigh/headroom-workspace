recipe(
  name = "ai.headline",
  doc = "Local AI models loaded and the memory they hold.",
  tags = ["ai", "headroom"],
)

def run():
  m = ai.ollama()
  l = try ai.lmstudio_ps
  n = count(m) + count(l)
  held = num(m | group {} { sum(memory) as m }) or 0
  return record(models_loaded = n, memory_held = Bytes(int(held)))
