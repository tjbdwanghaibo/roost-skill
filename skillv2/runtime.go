package skillv2

import (
	"errors"
	"sync"
)

var (
	ErrAuthorityMismatch        = errors.New("skillv2: program and host authority mismatch")
	ErrProgramSemanticsMismatch = errors.New("skillv2: compiler semantics revision is unsupported")
	ErrCastInputInvalid         = errors.New("skillv2: cast input does not match program input layout")
	ErrProgramInvariant         = errors.New("skillv2: immutable program invariant failed")
	ErrAsyncFlowNotScheduled    = errors.New("skillv2: asynchronous flow is not scheduled")
	ErrReverseAdvance           = errors.New("skillv2: cannot advance runtime backwards")
	ErrCooldownActive           = errors.New("skillv2: skill is on cooldown")
	ErrCastInputRejected        = errors.New("skillv2: cast does not accept gameplay input in its current state")
)

const supportedCompilerSemanticsRevision = "skillv2-compiler-1"

type RuntimeOptions struct {
	MatchSeed                          [32]byte
	SupportedCompilerSemanticsRevision string
	PassiveRouter                      PassiveRouter
	MaxPassiveActivationsPerTick       int
	MaxOwnedProcesses                  int
	MaxOwnedProcessesPerOwner          int
	MaxOwnedProcessesPerProgram        int
	MaxOwnedProcessesPerTemplate       int
	TraceSink                          TraceSink
	TraceLimits                        TraceLimits
	// TraceLimit is retained for compatibility. New callers should use
	// TraceLimits.MaxBuffer.
	TraceLimit int
	// PresentationLimit bounds renderer-facing events retained for polling.
	PresentationLimit int
	// StateEventLimit bounds authoritative change events retained for sync.
	StateEventLimit int
	// StateMutationLimit bounds canonical, client-applicable state mutations.
	StateMutationLimit int
}

type CastInput struct {
	Caster        EntityID
	Target        EntityID
	Position      *Position
	Direction     *Direction
	StartPosition *Position
	EndPosition   *Position
	Path          []Position
}

type InputPayload struct {
	Target        EntityID
	Position      *Position
	Direction     *Direction
	StartPosition *Position
	EndPosition   *Position
	Path          []Position
}

type CastStatus string

const (
	CastRunning   CastStatus = "running"
	CastSuspended CastStatus = "suspended"
	CastFinished  CastStatus = "finished"
	CastFailed    CastStatus = "failed"
)

type CastWindowStage string

const (
	CastWindowPreparing  CastWindowStage = "preparing"
	CastWindowCommitted  CastWindowStage = "committed"
	CastWindowExecuting  CastWindowStage = "executing"
	CastWindowRecovering CastWindowStage = "recovering"
	CastWindowComplete   CastWindowStage = "complete"
	CastWindowCancelled  CastWindowStage = "cancelled"
)

type CastSnapshot struct {
	ID              CastID
	Caster          EntityID
	Status          CastStatus
	CurrentPhase    PhaseIndex
	VisibleRevision WorldRevision
	Failure         string
	Events          []RuntimeEvent
	WindowStage     CastWindowStage
	Committed       bool
	ElapsedTicks    Tick
	PulseIndex      int64
	ReleaseReason   string
	Stock           int64
	MaxStock        int64
}

type castInstance struct {
	id                 CastID
	program            *Program
	caster             EntityID
	primaryTarget      EntityID
	inputs             []RuntimeValue
	memory             []RuntimeValue
	locals             []RuntimeValue
	snapshots          map[int]RuntimeValue
	status             CastStatus
	currentPhase       PhaseIndex
	visibleRevision    WorldRevision
	failure            string
	events             []RuntimeEvent
	randomKey          [32]byte
	randomInvocations  map[RandomSiteIndex]uint64
	eventContext       EventContext
	phaseToken         uint64
	pendingTasks       int
	logicalFinished    bool
	areaCallbackFinish bool
	windowStage        CastWindowStage
	startTick          Tick
	committed          bool
	costsPaid          bool
	cooldownStarted    bool
	pulseIndex         int64
	releaseReason      string
	stock              int64
	maxStock           int64
	windowStartTick    Tick
	pendingRootEvent   string
	policyActive       bool
	cooldownOwner      EntityID
	ability            AbilityHandle
	abilityFinished    bool
	detachedProcess    *ProcessInstance
	detachedEvent      EventContext
}

type cooldownKey struct {
	Caster EntityID
	Skill  string
}

type skillStateKey struct {
	Caster EntityID
	Skill  string
}

type skillState struct {
	stock              int64
	maxStock           int64
	rechargeTicks      Tick
	rechargeDue        Tick
	rechargeScheduled  bool
	rechargeGeneration uint64
}

type Runtime struct {
	mutex                   sync.Mutex
	host                    Host
	options                 RuntimeOptions
	casts                   map[CastID]*castInstance
	nextCastID              CastID
	eventCursor             EventCursor
	currentTick             Tick
	scheduler               *scheduler
	nextTaskSequence        uint64
	frames                  map[FrameID][]RuntimeValue
	nextFrameID             FrameID
	processes               map[ProcessID]*ProcessInstance
	ownedProcesses          map[ProcessID]*ProcessInstance
	nextProcessID           ProcessID
	cooldowns               map[cooldownKey]Tick
	skillStates             map[skillStateKey]*skillState
	activePolicies          map[skillStateKey]CastID
	nextPassiveActivationID PassiveActivationID
	procLedger              map[procLedgerKey]struct{}
	rootEventCounts         map[EventID]int
	passiveCountTick        Tick
	passiveCount            int
	runtimeEvents           []RuntimeEvent
	abilities               map[abilityKey]*abilityState
	abilityByProgram        map[skillStateKey]AbilityHandle
	nextAbilityHandle       AbilityHandle
	nextAbilityOverlay      uint64
	trace                   []TraceEvent
	traceSequence           uint64
	traceTruncated          bool
	traceFlushed            int
	presentationEvents      []PresentationEvent
	presentationSequence    uint64
	stateEvents             []StateEvent
	stateEventSequence      uint64
	stateEventDropped       uint64
	stateMutations          []StateMutation
	stateMutationSequence   uint64
	stateMutationDropped    uint64
	stateMutationBaseline   RuntimeStateSnapshot
	stateMutationReady      bool
}

func NewRuntime(host Host, options RuntimeOptions) *Runtime {
	if options.SupportedCompilerSemanticsRevision == "" {
		options.SupportedCompilerSemanticsRevision = supportedCompilerSemanticsRevision
	}
	if options.MaxPassiveActivationsPerTick <= 0 {
		options.MaxPassiveActivationsPerTick = 256
	}
	if options.MaxOwnedProcesses <= 0 {
		options.MaxOwnedProcesses = 128
	}
	if options.MaxOwnedProcessesPerOwner <= 0 {
		options.MaxOwnedProcessesPerOwner = options.MaxOwnedProcesses
	}
	if options.MaxOwnedProcessesPerProgram <= 0 {
		options.MaxOwnedProcessesPerProgram = options.MaxOwnedProcesses
	}
	if options.MaxOwnedProcessesPerTemplate <= 0 {
		options.MaxOwnedProcessesPerTemplate = options.MaxOwnedProcesses
	}
	if options.PresentationLimit <= 0 {
		options.PresentationLimit = 1024
	}
	if options.StateEventLimit <= 0 {
		options.StateEventLimit = 2048
	}
	if options.StateMutationLimit <= 0 {
		options.StateMutationLimit = 2048
	}
	runtime := &Runtime{
		host: host, options: options,
		casts: make(map[CastID]*castInstance), scheduler: newScheduler(),
		frames: make(map[FrameID][]RuntimeValue), processes: make(map[ProcessID]*ProcessInstance), ownedProcesses: make(map[ProcessID]*ProcessInstance),
		cooldowns:   make(map[cooldownKey]Tick),
		skillStates: make(map[skillStateKey]*skillState), activePolicies: make(map[skillStateKey]CastID),
		procLedger: make(map[procLedgerKey]struct{}), rootEventCounts: make(map[EventID]int),
		abilities: make(map[abilityKey]*abilityState), abilityByProgram: make(map[skillStateKey]AbilityHandle),
	}
	if host != nil {
		for _, event := range host.Events(0) {
			if event.Cursor > runtime.eventCursor {
				runtime.eventCursor = event.Cursor
			}
		}
	}
	runtime.stateMutationBaseline = runtime.stateSnapshotLocked()
	runtime.stateMutationReady = true
	return runtime
}

func (runtime *Runtime) Activate(program *Program, input CastInput) (CastID, error) {
	return runtime.Start(program, input)
}

func (runtime *Runtime) Start(program *Program, input CastInput) (CastID, error) {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return runtime.startLocked(program, input, nil)
}

func (runtime *Runtime) startLocked(program *Program, input CastInput, parentEvent *EventContext) (CastID, error) {
	if program == nil || runtime.host == nil {
		return 0, ErrProgramInvariant
	}
	if program.compilerSemanticsRevision != runtime.options.SupportedCompilerSemanticsRevision {
		return 0, ErrProgramSemanticsMismatch
	}
	if !authorityMatches(program.authority, runtime.host.AuthorityIdentity()) {
		return 0, ErrAuthorityMismatch
	}
	inputs, err := freezeCastInput(program, input, runtime.host)
	if err != nil {
		return 0, err
	}
	ability, abilityErr := runtime.ensureAbilityLocked(input.Caster, program)
	if abilityErr != nil {
		return 0, abilityErr
	}
	policyKey := skillStateKey{Caster: input.Caster, Skill: program.id}
	if program.cast.mode == castModeToggle {
		if activeID := runtime.activePolicies[policyKey]; activeID != 0 {
			active := runtime.casts[activeID]
			if active != nil && active.policyActive {
				return activeID, runtime.releaseCast(active, "toggle_off")
			}
			delete(runtime.activePolicies, policyKey)
		}
	}
	tentativeID := runtime.nextCastID + 1
	cast := &castInstance{
		id: tentativeID, program: program, caster: input.Caster, primaryTarget: input.Target,
		inputs: inputs, memory: make([]RuntimeValue, len(program.memory)), locals: make([]RuntimeValue, len(program.locals)),
		snapshots: make(map[int]RuntimeValue), status: CastRunning, currentPhase: program.initialPhase,
		visibleRevision: runtime.host.CurrentRevision(), randomInvocations: make(map[RandomSiteIndex]uint64), phaseToken: 1,
	}
	cast.ability = ability.handle
	cast.cooldownOwner = input.Caster
	if parentEvent != nil && program.cooldownScope == "target" && parentEvent.Target != 0 {
		cast.cooldownOwner = parentEvent.Target
	}
	cast.randomKey = deriveCastRandomKey(runtime.options.MatchSeed, program.identity.gameplayDigest, input.Caster, uint64(tentativeID))
	if parentEvent == nil {
		cast.eventContext = newRootEvent(EventID(tentativeID))
	} else {
		cast.eventContext = deriveEvent(*parentEvent, EventID((uint64(tentativeID)<<32)|1))
		cast.eventContext.ProcDepth = parentEvent.ProcDepth + 1
	}
	cast.eventContext.Source, cast.eventContext.Owner, cast.eventContext.Target, cast.eventContext.SkillID, cast.eventContext.CastID = input.Caster, input.Caster, input.Target, program.id, tentativeID
	for index, slot := range program.memory {
		value, evalErr := runtime.evalValue(cast, slot.defaultValue)
		if evalErr != nil {
			return 0, evalErr
		}
		cast.memory[index] = value
	}
	if err := runtime.captureSnapshots(cast, snapshotCastStart); err != nil {
		return 0, err
	}
	runtime.nextCastID = tentativeID
	runtime.casts[cast.id] = cast
	runtime.recordTrace(TraceEvent{Kind: TraceCastActivated, Tick: runtime.currentTick, CastID: cast.id})
	runtime.markAbilityCastStarted(cast)
	if err := runtime.prepareCast(cast); err != nil {
		cast.status, cast.failure = CastFailed, err.Error()
		_ = runtime.stopProcesses(cast, true)
		runtime.markAbilityCastFinished(cast)
		if !cast.committed {
			delete(runtime.casts, cast.id)
			runtime.nextCastID--
			return 0, err
		}
		return cast.id, err
	}
	runtime.recordTrace(TraceEvent{Kind: TraceCastPrepared, Tick: runtime.currentTick, CastID: cast.id})
	return cast.id, nil
}

func (runtime *Runtime) CastCount() int {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return len(runtime.casts)
}

func (runtime *Runtime) InspectCast(id CastID) (CastSnapshot, bool) {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	cast, ok := runtime.casts[id]
	if !ok {
		return CastSnapshot{}, false
	}
	return CastSnapshot{
		ID: cast.id, Caster: cast.caster, Status: cast.status, CurrentPhase: cast.currentPhase,
		VisibleRevision: cast.visibleRevision, Failure: cast.failure, Events: cloneRuntimeEvents(cast.events),
		WindowStage: cast.windowStage, Committed: cast.committed, ElapsedTicks: runtime.currentTick - cast.startTick,
		PulseIndex: cast.pulseIndex, ReleaseReason: cast.releaseReason, Stock: cast.stock, MaxStock: cast.maxStock,
	}, true
}
