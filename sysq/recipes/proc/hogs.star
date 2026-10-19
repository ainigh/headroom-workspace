recipe(
  name = "proc.hogs",
  doc = "Sustained CPU or memory offenders: processes above a threshold in every sample of a short watch.",
  params = {
    "cpu": param(float, default = 50.0),
    "mem": param(Bytes, default = 4GB),
    "samples": param(int, default = 3, min = 2, max = 10),
  },
  tags = ["cpu", "memory", "deep_dive"],
)

def run(cpu, mem, samples):
  s = watch(proc.top(by = "cpu", n = 30), every = 2s, for_ = Duration(2 * (samples - 1)))
  g = s | group by pid, command { count() as seen, avg(cpu) as avg_cpu, max(mem) as max_mem, min(cpu) as min_cpu }
  return g | filter seen == samples and (min_cpu >= cpu or max_mem >= mem) | sort by avg_cpu desc
