# Kickoff prompt for Claude Code

Do not modify

paste this as the first message:

---

Read CLAUDE.md, then every file in docs/ in the order CLAUDE.md lists, then the three component CLAUDE.md files. Don't write code until you've done that.

Then:
1. Summarize back to me in under 200 words: the ten non-negotiables from docs/sysq-design.md §23, and the milestone you'll start with.
2. List any contradiction or gap you found between the docs. If none block M0, proceed; otherwise ask.
3. Initialize the repo (git init, Go modules for sysq and sysact, Swift package + app skeleton for headroom), commit as "M0: scaffold".
4. Start M0 per docs/implementation.md §6. Build the fixture capture tool first and capture real output from this Mac for the 12 M0 primitives. Write the sandbox write-denial test before the executor.
5. When M0's "done when" condition passes, tick it in TASKS.md, commit, and give me a short report: what works, what you decided (also in DECISIONS.md), what's next. Then continue to M1 without waiting unless something needs my input.

Standing rules for the whole project:
- Stop and ask if a task would require a raw-shell tool, an auto-approve path, declared recipe cost/sensitivity, or any other MUST violation.
- Keep going milestone by milestone; report at each boundary.
- If you need sudo for powermetrics primitives, don't configure sudoers yourself — tell me the exact rule and I'll add it.
- If you need a running Ollama model for M8, tell me which model size you want loaded.

---

## Useful follow-up prompts

- "Run the full sysq catalog under the sandbox and show me the denial log."
- "Show me the lowered Starlark for: `proc.top(by: "cpu") | filter cpu > 30 | limit 5`"
- "Propose three recipes missing from the starter library as files in sysq/recipes/proposed/, two lines each. Don't promote them."
- "Audit headroom/ for any direct use of SysqClient outside HeadroomKit stores."
- "Profile idle CPU and RSS of sysqd + Headroom for 10 minutes and report against the M9 budget."
