module github.com/ainigh/headroom-workspace/sysact

go 1.25.0

require (
	github.com/ainigh/headroom-workspace/sysq v0.0.0
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/ainigh/headroom-workspace/sysq => ../sysq
