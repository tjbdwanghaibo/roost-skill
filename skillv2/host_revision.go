package skillv2

import "errors"

type WorldRevision uint64
type EventCursor uint64
type ProcessID uint64

var (
	ErrRevisionUnavailable       = errors.New("skillv2: required world revision is unavailable")
	ErrEntityNotFound            = errors.New("skillv2: entity not found")
	ErrInsufficientResource      = errors.New("skillv2: insufficient resource")
	ErrRuntimeValueMissing       = errors.New("skillv2: runtime value is missing")
	ErrRuntimeTypeMismatch       = errors.New("skillv2: runtime value type mismatch")
	ErrRuntimeQuantityMismatch   = errors.New("skillv2: runtime quantity mismatch")
	ErrRuntimeArithmeticOverflow = errors.New("skillv2: runtime arithmetic overflow")
	ErrCombatHandleInvalid       = errors.New("skillv2: combat handle is not authorized")
	ErrCombatPolicyUnsupported   = errors.New("skillv2: combat policy is unsupported")
	ErrHostContractViolation     = errors.New("skillv2: host contract violation")
)

type QueryMeta struct {
	RequiredRevision WorldRevision
}

type QueryResultMeta struct {
	Revision WorldRevision
}

type CommandMeta struct {
	RequiredRevision WorldRevision
	EffectIndex      EffectIndex
}

type CommitReceipt struct {
	Revision WorldRevision
	Changed  bool
}

type RuntimeEvent struct {
	Cursor    EventCursor
	Revision  WorldRevision
	Tick      Tick
	Kind      string
	Entity    EntityID
	ProcessID ProcessID
	Context   EventContext
	State     *StateChangeEvent
	Ability   *AbilityChangeEvent
	Result    *EffectResultEvent
}
