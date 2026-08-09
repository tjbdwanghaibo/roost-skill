# cube-skill

`cube-skill` is the reusable skill compiler and authoritative runtime shared by
Cube applications. It has no dependency on a concrete game server, renderer,
or transport.

Packages:

- `skillv2`: strict wire format, compiler, immutable program, runtime, replay,
  presentation plans, and presentation events.
- `skillcompose`: composition contracts and policy helpers built on `skillv2`.
- `skillsync`: strongly typed records, server coordinator, recovery integration,
  and client applier for the generic `cube-core/syncstream` protocol.

The host application owns gameplay catalogs and implements `skillv2.Host`.
Clients consume `PresentationPlan` once per program identity and incremental
`PresentationEvent` records during a match.

Start with [the implementation learning guide](docs/skill-v2-current-implementation-learning.md)
and [the Visual/sync production guide](docs/visual-sync-production-guide.md).
