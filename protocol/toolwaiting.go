package protocol

// ToolWaiting is the Tool of a ToolPhaseStep activity saying that a model call
// is waiting out the provider's rate limit before it tries again (2026-10-25).
// DurationMS is how long the wait is.
//
// IT IS A MOMENT, NOT AN EVENT. Once the wait is over it is no longer true, so
// a client shows it while it lasts -- the terminal client where it shows
// "thinking" -- rather than as a line left behind in the conversation.
const ToolWaiting = "waiting"
