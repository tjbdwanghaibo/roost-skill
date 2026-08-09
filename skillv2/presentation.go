package skillv2

// PresentationEventKind describes a renderer-facing event. Presentation data
// is deliberately separate from gameplay RuntimeEvent and TraceEvent streams:
// it may be dropped and replayed without changing authoritative simulation.
type PresentationEventKind string

const (
	PresentationCast   PresentationEventKind = "cast"
	PresentationEffect PresentationEventKind = "effect"
)

// PresentationMount connects an immutable visual manifest entry to a compiled
// gameplay mount point.
type PresentationMount struct {
	VisualIndex     VisualIndex
	EffectIndex     EffectIndex
	HasEffect       bool
	ProcessTemplate ProcessTemplateIndex
	HasProcess      bool
}

// PresentationPlan is the stable, cacheable renderer contract for a Program.
// A client should key it by Identity.PresentationDigest.
type PresentationPlan struct {
	Identity ProgramIdentityView
	Manifest SkillVisualManifest
	Cast     *PresentationMount
	Effects  []PresentationMount
}

// InspectPresentationPlan returns a detached projection of the immutable
// compiled program. Mutating the returned slices cannot mutate the Program.
func InspectPresentationPlan(program *Program) PresentationPlan {
	if program == nil {
		return PresentationPlan{}
	}
	plan := PresentationPlan{Identity: InspectIdentity(program), Manifest: InspectVisualManifest(program)}
	if program.hasCastVisual {
		plan.Cast = &PresentationMount{VisualIndex: program.castVisual}
	}
	for _, operation := range program.operations {
		continuations, effectIndex, ok := operationEffectContinuations(operation)
		if !ok || !continuations.hasVisual {
			continue
		}
		plan.Effects = append(plan.Effects, PresentationMount{
			VisualIndex: continuations.visual, EffectIndex: effectIndex, HasEffect: true,
			ProcessTemplate: continuations.processTemplate, HasProcess: continuations.hasProcess,
		})
	}
	return plan
}

// PresentationEvent is an ordered runtime instruction for clients. Sequence is
// local to one Runtime and WorldRevision identifies the authoritative state the
// event was committed against.
type PresentationEvent struct {
	Sequence           uint64
	Tick               Tick
	WorldRevision      WorldRevision
	Kind               PresentationEventKind
	ProgramID          string
	GameplayDigest     string
	PresentationDigest string
	CastID             CastID
	EffectIndex        EffectIndex
	HasEffect          bool
	VisualIndex        VisualIndex
	Source             EntityID
	PrimaryTarget      EntityID
	ProcessTemplate    ProcessTemplateIndex
	HasProcess         bool
}

// PresentationEvents returns events with Sequence greater than after. Returned
// storage is detached from the runtime buffer.
func (runtime *Runtime) PresentationEvents(after uint64) []PresentationEvent {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	index := 0
	for index < len(runtime.presentationEvents) && runtime.presentationEvents[index].Sequence <= after {
		index++
	}
	return append([]PresentationEvent(nil), runtime.presentationEvents[index:]...)
}

func (runtime *Runtime) emitCastPresentation(cast *castInstance) {
	if cast == nil || cast.program == nil || !cast.program.hasCastVisual {
		return
	}
	runtime.appendPresentation(cast, PresentationEvent{
		WorldRevision: runtime.host.CurrentRevision(), Kind: PresentationCast,
		VisualIndex: cast.program.castVisual,
	})
}

func (runtime *Runtime) emitEffectPresentation(cast *castInstance, continuations effectContinuations, effectIndex EffectIndex, revision WorldRevision) {
	if cast == nil || cast.program == nil || !continuations.hasVisual {
		return
	}
	runtime.appendPresentation(cast, PresentationEvent{
		WorldRevision: revision, Kind: PresentationEffect,
		EffectIndex: effectIndex, HasEffect: true, VisualIndex: continuations.visual,
		ProcessTemplate: continuations.processTemplate, HasProcess: continuations.hasProcess,
	})
}

func (runtime *Runtime) appendPresentation(cast *castInstance, event PresentationEvent) {
	runtime.presentationSequence++
	event.Sequence = runtime.presentationSequence
	event.Tick = runtime.currentTick
	event.ProgramID = cast.program.id
	event.GameplayDigest = cast.program.identity.gameplayDigest
	event.PresentationDigest = cast.program.identity.presentationDigest
	event.CastID = cast.id
	event.Source = cast.caster
	event.PrimaryTarget = cast.primaryTarget
	runtime.presentationEvents = append(runtime.presentationEvents, event)
	limit := runtime.options.PresentationLimit
	if overflow := len(runtime.presentationEvents) - limit; overflow > 0 {
		copy(runtime.presentationEvents, runtime.presentationEvents[overflow:])
		runtime.presentationEvents = runtime.presentationEvents[:limit]
	}
}
