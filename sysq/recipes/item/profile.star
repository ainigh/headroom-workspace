recipe(
  name = "item.profile",
  doc = "Everything about one item — a process (pid), an app or process name, a path, or a local model — across every resource group.",
  params = {
    "kind": param(str, values = ["process", "app", "path", "model"], default = "process"),
    "id": param(str, required = True, max_len = 512),
  },
  tags = ["item", "headroom"],
  example = "item.profile(kind=\"process\", id=\"2210\")",
)

def process(pid):
  info = try proc.info(pid = pid)
  top = proc.top(by = "cpu", n = 50) | filter pid == pid | select cpu, mem, power
  conns = try net.connections | filter pid == pid
  usage = record(memory = info.rss[0] if count(info) > 0 else None,
                 cpu = top.cpu[0] if count(top) > 0 else None,
                 energy = top.power[0] if count(top) > 0 else None,
                 connections = count(conns))
  return {"summary": info | select pid, ppid, user, command, started, rss, cpu, now() - started as uptime, "usage": usage, "connections": conns | limit 20}

def run(kind, id):
  if kind == "process":
    return process(int(id))
  if kind == "app":
    procs = proc.list | filter name == id or name contains id | select pid, name, rss, cpu, started
    total = procs | group {} { count() as processes, sum(rss) as memory, sum(cpu) as cpu }
    return {"summary": total, "processes": procs | sort by rss desc | limit 20}
  if kind == "path":
    st = try fs.stat(Path(id))
    size = try disk.usage(Path(id), depth = 0)
    return {"summary": st, "size": size}
  models = ai.ollama() | filter model == id
  files = try ai.model_files | filter model == id | group by model { sum(size) as size }
  return {"loaded": models, "on_disk": files}
