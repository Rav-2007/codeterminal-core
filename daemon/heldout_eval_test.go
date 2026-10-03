//go:build eval

// THE HELD-OUT QUESTIONS: what the retrieval levers of 2026-10 were NOT tuned on.
//
// WHY THIS FILE EXISTS. The 49 queries in rerank_eval_test.go have chosen every
// retrieval lever this daemon has -- k, the character budget, neighbour and
// construct widening, the path in the embedded text, the class weights. A lever
// picked on those 49 and confirmed only on those 49 can simply be fitting them,
// and at 49 queries one flip is two points: the set is too small to tell a real
// gain from a lucky one by itself.
//
// So these are a second set with three properties the first cannot have:
//
//   - WRITTEN BEFORE ANY 2026-10 CHANGE WAS RUN AGAINST THEM (pre-registered
//     2026-10-03). Their wording was fixed from what each feature does, before
//     the code that answers it was opened, so no question borrows its answer's
//     vocabulary.
//   - ABOUT CODE NONE OF THE 49 ASK ABOUT -- mostly subsystems that did not
//     exist when those were written: the long-task engine, the working copy,
//     the key prompt, the sandbox home reclaim.
//   - NEVER USED TO CHOOSE A POLICY. They are scored beside the 49 and only
//     ever asked whether a choice made on the 49 holds on questions it never
//     saw.
//
// THE RULE, written down before the first run: a retrieval arm ships only if
//
//   - DELIVERED on the 49 is at least the shipped row's plus 2,
//   - DELIVERED on these is at least the shipped row's,
//   - no shape of the 49 falls by more than 1,
//   - it was measured inside one index build, and
//   - each lever in a combination does no harm on either set by itself.
//
// NOTHING ELSE MAY QUOTE THESE. A document pairing one of these questions with
// its answer file would be an answer key inside the corpus -- which is how
// RETRIEVAL_BUDGET_DESIGN.md spent six weeks grading its own homework. This file
// is held out of the index (evalSelfReferenceFiles), the leak guard scans the
// corpus for every string below, and no write-up names them or their answers.
package main

// shapeAgent is a query phrased the way a MODEL calls search_code rather than
// the way a person asks: identifiers it has already seen, subsystem nouns, no
// sentence. Agent mode is where most searches now come from -- an agent turn
// attaches no code up front and searches for itself (gatherDirectRefs) -- and
// none of the 49 is shaped like that.
const shapeAgent = "agent"

var heldOutEvalQueries = []rerankEvalQuery{
	{"how does a long task save its progress so it can be resumed after a restart",
		[]string{"daemon/taskledger.go"},
		[]string{"func (l *taskLedger) save("}, shapeImpl},

	{"what stops a long task from finishing before its checks have passed",
		[]string{"daemon/longtask.go"},
		[]string{"func finishRefusal("}, shapeImpl},

	{"how does the working copy avoid following a link that a command planted",
		[]string{"daemon/stage.go"},
		[]string{"func (st *stagedWorkspace) readCopyFile("}, shapeImpl},

	{"where is a checkpoint of the working copy restored",
		[]string{"daemon/checkpoint.go"},
		[]string{"func (st *stagedWorkspace) restoreCheckpoint("}, shapeImpl},

	{"how are quoted file names in git output turned back into paths",
		[]string{"daemon/githistory.go"},
		[]string{"func unquoteGitPath("}, shapeImpl},

	{"what limits how much output the grep tool can return",
		[]string{"daemon/grep.go"},
		[]string{"grepMaxMatches     = 200"}, shapeImpl},

	{"what happens when the model's reply stream goes silent",
		[]string{"daemon/provider.go"},
		[]string{"var streamStallTimeout = 60 * time.Second"}, shapeImpl},

	{"how does the daemon tell that a message is just a greeting",
		[]string{"daemon/smalltalk.go"},
		[]string{"func isSmallTalk("}, shapeImpl},

	{"what prevents a fetched web page from reaching a private network address",
		[]string{"daemon/webfetch.go"},
		[]string{"Control: func(network, address string, _ syscall.RawConn) error {"}, shapeImpl},

	{"how is the terminal put back to normal if the key prompt is interrupted",
		[]string{"daemon/termecho_unix.go"},
		[]string{"signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)"}, shapeImpl},

	{"how long does connect wait for the provider to confirm a key",
		[]string{"daemon/connect_cmd.go"},
		[]string{"var connectVerifyTimeout = 15 * time.Second"}, shapeImpl},

	{"how does a resumed chat keep the time each turn originally happened",
		[]string{"daemon/chatarchive.go"},
		[]string{"func archiveTime("}, shapeImpl},

	{"what evidence is required before a spec criterion counts as met",
		[]string{"daemon/spec.go"},
		[]string{"func (s *Server) builtinRecordCriterion("}, shapeImpl},

	// Asks about an edit, which the daemon proposes; the display rule lives in
	// the editapply module.
	{"how is the path of an edit outside the project shown to the user",
		[]string{"editapply/outside.go"},
		[]string{"func HomeDisplayPath("}, shapeCross},

	// REPLACED BEFORE ANY RUN, and the replacement is itself a finding. This
	// slot asked how the API key is stored on disk, and the answer --
	// daemon/credentials.go -- can never be retrieved: its name contains
	// "credential", so the secret-name gate (editapply.MatchesSecretName) keeps
	// the whole file out of the index, as it does credentialverify.go and
	// editapply/secret.go. That is a deliberate over-refusal on names, and a
	// security decision rather than a retrieval one, so the question was
	// swapped for an answerable one instead of the gate being touched.
	{"what happens when two daemons start for the same workspace at once",
		[]string{"daemon/main.go", "daemon/exitcodes.go"},
		[]string{"os.Exit(exitAlreadyRunning)", "exitAlreadyRunning = 3"}, shapeMulti},

	{"how does the daemon catch up on files that changed while it was not running",
		[]string{"daemon/reindex.go"},
		[]string{"func (s *Server) catchUpIndex("}, shapeImpl},

	// Two places shorten it, and both answer the question as asked: a
	// command's output is capped at the source, and every tool result is
	// capped again on its way into the conversation.
	{"where are long command outputs shortened before the model sees them",
		[]string{"daemon/mcp_exec.go", "daemon/toolresult.go"},
		[]string{"type tailBuffer struct", "func renderToolResult"}, shapeMulti},

	{"when may the sandbox run programs beyond the default list",
		[]string{"daemon/mcp_exec.go"},
		[]string{"len(extras) > 0 && s.sandboxExecConfined()"}, shapeImpl},

	{"how is a follow-up message like continue searched for code",
		[]string{"daemon/context.go"},
		[]string{"func retrievalQueryFor("}, shapeImpl},

	{"where do stale sandbox home directories get cleaned up",
		[]string{"daemon/sandboxhome.go"},
		[]string{"func reclaimSandboxHomes("}, shapeImpl},

	// ── Agent-shaped: how a model searches, not how a person asks ──────────

	// The in-process lock and the file lock under it are one answer in two
	// halves.
	{"applyLocks flock across processes",
		[]string{"daemon/server_workspace_lock.go", "editapply/applylock.go", "editapply/applylock_unix.go"},
		[]string{"func (s *Server) lockWorkspace(", "func LockWorkspaceApply(",
			"syscall.Flock(int(f.Fd()), syscall.LOCK_EX)"}, shapeAgent},

	{"TIOCGETA TCGETS termios ioctl",
		[]string{"daemon/termecho_ioctl_bsd.go", "daemon/termecho_ioctl_other.go"},
		[]string{"ioctlReadTermios  = unix.TIOCGETA", "ioctlReadTermios  = unix.TCGETS"}, shapeAgent},

	{"ledger Summary final reply finish_task",
		[]string{"daemon/longtask.go"},
		[]string{`if sum := run.ledger.Summary; sum != "" {`}, shapeAgent},

	{"investigate roleResearcher sub loop summary",
		[]string{"daemon/investigate.go"},
		[]string{"func (s *Server) newInvestigator("}, shapeAgent},

	{"Landlock ruleset restrict filesystem",
		[]string{"daemon/mcp/sandbox_landlock_linux.go"},
		[]string{"func applySandbox(policy landlockPolicy"}, shapeAgent},

	{"trimDriveSlash uriPath",
		[]string{"daemon/rename.go"},
		[]string{"func trimDriveSlash("}, shapeAgent},

	{"warnSink jsonl",
		[]string{"daemon/warnsink.go"},
		[]string{"func (w *warnSink) write("}, shapeAgent},

	{"rememberPrompt connectWithKey",
		[]string{"clients/tui/chat.go", "clients/tui/prompthistory.go"},
		[]string{"if !connectWithKey(raw) {", "func (m *chatModel) rememberPrompt("}, shapeAgent},
}
