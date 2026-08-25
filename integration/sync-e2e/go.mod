module github.com/tjbdwanghaibo/cube-skill/v2/integration/sync-e2e

go 1.25.0

require (
	github.com/tjbdwanghaibo/cube-core v1.3.0
	github.com/tjbdwanghaibo/cube-kit v1.1.0
	github.com/tjbdwanghaibo/cube-skill/v2 v2.0.0
)

// Replacements are confined to this integration-test module.
replace github.com/tjbdwanghaibo/cube-core => ../../../cube-core

replace github.com/tjbdwanghaibo/cube-kit => ../../../cube-kit

replace github.com/tjbdwanghaibo/cube-skill/v2 => ../..
