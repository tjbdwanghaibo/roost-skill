module github.com/tjbdwanghaibo/cube-skill/v2/integration/sync-e2e

go 1.25.0

require (
	github.com/tjbdwanghaibo/cube-core v1.6.2
	github.com/tjbdwanghaibo/cube-kit v1.6.1
	github.com/tjbdwanghaibo/cube-skill/v2 v2.0.0
)

// The replacement is confined to this integration-test module: it exists to
// exercise the working-tree cube-skill against released cube-core/cube-kit.
replace github.com/tjbdwanghaibo/cube-skill/v2 => ../..
