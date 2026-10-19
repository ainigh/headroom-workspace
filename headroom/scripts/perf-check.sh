#!/bin/bash
# M9 "done when": Headroom + sysqd idle under 1 % CPU and under 60 MB RSS combined.
# Samples both with ps for a while (default 60 s) with no Headroom window open, and reports.
#
#   headroom/scripts/perf-check.sh [seconds]
set -euo pipefail
secs="${1:-60}"
pids="$(pgrep -x Headroom || true) $(pgrep -x sysqd || true)"
[[ -n "${pids// /}" ]] || { echo "Headroom and sysqd aren't running" >&2; exit 2; }
echo "sampling $secs s: $(echo $pids | tr ' ' ',')"
cpu_sum=0; rss_max=0; n=0
for ((i = 0; i < secs; i += 2)); do
  read -r cpu rss < <(ps -o %cpu=,rss= -p $(echo $pids | tr ' ' ',') | awk '{c+=$1; r+=$2} END {print c, r}')
  cpu_sum=$(echo "$cpu_sum + $cpu" | bc); n=$((n + 1))
  (( rss > rss_max )) && rss_max=$rss
  sleep 2
done
avg=$(echo "scale=2; $cpu_sum / $n" | bc)
mb=$((rss_max / 1024))
echo "average CPU ${avg}% (budget 1%), peak RSS ${mb} MB (budget 60 MB)"
ok=1
(( $(echo "$avg < 1" | bc) )) || ok=0
(( mb < 60 )) || ok=0
[[ $ok == 1 ]] && echo "PASS" || { echo "FAIL"; exit 1; }
