package skillv2

func (runtime *Runtime) drainHostEvents(cast *castInstance) {
	events := runtime.host.Events(runtime.eventCursor)
	for _, event := range events {
		if event.Cursor > runtime.eventCursor {
			runtime.eventCursor = event.Cursor
		}
		cast.events = append(cast.events, cloneRuntimeEvent(event))
		_ = runtime.dispatchEvent(event.Context)
	}
}

func cloneRuntimeEvent(event RuntimeEvent) RuntimeEvent {
	event.Context = cloneEventContext(event.Context)
	if event.State != nil {
		state := *event.State
		state.Before = cloneStateRuntimeValue(state.Before)
		state.After = cloneStateRuntimeValue(state.After)
		event.State = &state
	}
	if event.Ability != nil {
		ability := *event.Ability
		ability.Before = cloneStateRuntimeValue(ability.Before)
		ability.After = cloneStateRuntimeValue(ability.After)
		event.Ability = &ability
	}
	if event.Result != nil {
		result := *event.Result
		event.Result = &result
	}
	return event
}
