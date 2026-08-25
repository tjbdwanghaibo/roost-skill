# cube-skill

Current release line: **v2**. This is a breaking production upgrade and uses
the Go module path `github.com/tjbdwanghaibo/cube-skill/v2`. Read the
[v2 breaking-upgrade runbook](docs/breaking-upgrade-v2.md) before deployment;
v1 checkpoint, outbox, and composition-contract files are not accepted.

Production deployment and release gates: [docs/production-readiness.md](docs/production-readiness.md).

`cube-skill` is the reusable skill compiler and authoritative runtime shared by
Cube applications. It has no dependency on a concrete game server, renderer,
or transport.

Packages:

- `skillv2`: strict wire format, compiler, immutable program, runtime, replay,
  presentation plans, and presentation events.
- `combat`: zero-dependency combat content battery — attribute sets, buff
  containers (stacking, dispel tags, immunity, tenacity), and the twelve-stage
  fixed-point damage pipeline. The skillv2 `MemoryHost` runs on this exact
  code, so reference math and production math cannot diverge.
- `combatcomponent`: `combat` wired into cube-core entities — a DAO behind a
  `checkpoint.DirtyTracker`, mutators that record `nest.RecordUndo` inverses
  (rolled-back handlers leave state byte-identical), persistence marshal, and
  a `HostAdapter` implementing the combat effect surface of `skillv2.Host`.
- `skillcompose`: composition contracts and policy helpers built on `skillv2`.
- `skillsync`: strongly typed records, server coordinator, recovery integration,
  and client applier for the generic `cube-core/syncstream` protocol.

The host application owns gameplay catalogs and implements `skillv2.Host`.
Clients consume `PresentationPlan` once per program identity and incremental
`PresentationEvent` records during a match.

## Scope

`cube-skill` is a **2D authoritative combat runtime**. The following are
deliberate non-goals, not gaps:

- **No third axis.** Positions are 2D fixed-point world coordinates. Height,
  gravity, and volumetric collision belong to the host world; a host that
  models elevation projects it into the 2D plane (or resolves it inside
  `Select`/`Apply`) before answering runtime queries.
- **No navmesh or pathfinding.** Motion follows authored paths and steering
  in open space. Obstacle-aware routing is the host's job — the runtime asks
  for blocked-position facts through `InputPositionResolver` and world truth
  through `Select`, and stays deterministic either way.
- **No client prediction or rollback netcode.** The runtime is
  server-authoritative: clients render `PresentationEvent` streams and apply
  `StateMutation` records; they never simulate ahead. Perceived latency is
  the transport's and renderer's problem to hide (cast windows and windup
  presentation exist partly for this), not the runtime's to predict.

Determinism is the contract that makes the rest work: fixed-point integer
math only, HMAC-derived randomness, bit-exact replay, and checkpoint
recovery. Anything that would trade determinism for convenience is out of
scope by design.

## Host concurrency contract

All `skillv2.Host` methods are called with the Runtime lock held: calls are
serialized, must not re-enter the Runtime, must not block, and must be
deterministic at the given revision. See the `Host` doc comment in
`skillv2/host.go` for the full contract.

Start with [the implementation learning guide](docs/skill-v2-current-implementation-learning.md)
and [the Visual/sync production guide](docs/visual-sync-production-guide.md).
