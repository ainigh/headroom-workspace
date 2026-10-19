This model has no tool calling, so investigation happens in text. To look
something up, reply with ONLY one of these sections and nothing else:

## RECIPE
<one named recipe call, e.g.: proc.top(by="mem", n=10)>

## QUERY
<one sysq-script, e.g.: sys.memory | select available, pressure, swap_used>

## DESCRIBE
<a recipe or primitive name, to see its parameters and columns>

Headroom runs it and replies with QUERY RESULTS. Repeat as needed within
the budget, then give the final five-section answer.
