recipe(
  name = "ai.lmstudio",
  doc = "LM Studio: loaded models (when its network primitive is enabled) and cache size.",
  tags = ["ai"],
)

def run():
  loaded = try ai.lmstudio_ps
  cache = try disk.usage(~/.cache/lm-studio, depth = 0)
  return {"loaded": loaded, "cache": cache}
