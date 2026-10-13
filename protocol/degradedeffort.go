package protocol

// DegradedReasoningEffort: the turn asked for a thinking effort and it was
// NOT sent, because this provider has already refused the field for this
// model (requestDialect.noReasoningEffort latched earlier in this daemon's
// life). The answer is the model's own default effort.
//
// THE CONTROL STAYS, THE SILENCE GOES. The picker offers Low / Medium /
// High for every model, and for the models whose provider rejects
// reasoning_effort the daemon dropped the field, latched, and said nothing
// -- so three settings produced one behaviour and the user was left to
// conclude the control did nothing, or worse, that it did something. A
// control whose effect is nil is a bug; a control that says when its effect
// is nil is a fact the user can act on (choose another model, or stop
// paying attention to the picker for this one).
//
// Not an error, and not a refusal: the effort is a preference and the answer
// is not. The turn runs, which is the whole reason the latch exists.
//
// IN A FILE OF ITS OWN, away from the other Degraded constants in protocol.go,
// so that file keeps its lines where they were: workspacetag.go says why that
// matters and what it cost when it was not done.
const DegradedReasoningEffort = "reasoning_effort"
