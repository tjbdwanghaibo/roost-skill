package skillv2

type CastWindowDefinition struct {
	WindupTicks        Tick     `json:"windup_ticks"`
	CommitTick         Tick     `json:"commit_tick"`
	RecoveryTicks      Tick     `json:"recovery_ticks"`
	Movement           string   `json:"movement"`
	Turning            string   `json:"turning"`
	InterruptTags      []string `json:"interrupt_tags"`
	RefundBeforeCommit bool     `json:"refund_before_commit"`
}
