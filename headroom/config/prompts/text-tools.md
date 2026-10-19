This model has no tool calling, so investigation happens in text.
To run a query, reply with ONLY this section and nothing else:

## QUERY
<one sysq-script, e.g.: proc.top(by: "mem", n: 10) | select pid, command, mem>

Headroom runs it and replies with QUERY RESULTS. Repeat as needed within
the budget, then give the final five-section answer. To describe a recipe or
primitive, query: describe("name").
