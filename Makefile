# Developer entry points. CI runs the same scripts, so a green `make check`
# here means the same thing it means there.
#
# There is no root Go module -- only go.work -- so `./...` from the repo root
# fails outright ("directory prefix . does not contain modules listed in
# go.work"). Every target below either names modules explicitly or delegates to
# a script that does. Do not "simplify" any of this to a root-level ./... .

MODULES := daemon editapply proxy helper protocol clients/tui

.PHONY: help hooks hookcheck test race fmt vet crossvet lint ratchet errcheck fuzz check docs webview drill soak eval evalguard debtmarkers parity supplychain

help:
	@echo "make hooks    install the tracked git hooks (.githooks/) -- do this once"
	@echo "make hookcheck verify that install actually happened (also runs inside check)"
	@echo "make check    everything CI runs: build, fmt, vet, race, lint, ratchet, docs"
	@echo "make docs     every relative link in a tracked .md resolves"
	@echo "make race     go test -race across all six modules"
	@echo "make crossvet compile the windows& darwin files this machine never sees"
	@echo "make lint     staticcheck + ineffassign + bodyclose"
	@echo "make ratchet  per-package coverage floors"
	@echo "make errcheck per-module unchecked-error ceilings (a ratchet, not a gate)"
	@echo "make fuzz     30s per fuzz target (FUZZTIME=5m to search harder)"
	@echo "make eval     real-model retrieval eval (~15min, downloads a model) -- NOT in check"
	@echo "make drill    mid-stream SIGTERM drill against the real proxy binary"
	@echo "make soak     30-minute sustained-load run (DURATION=180 for a quick check)"

# Points git at the tracked hooks directory. git does not sync .git/hooks
# between clones, so a hook only becomes shared if it is tracked AND git is told
# where to look -- this target is the second half.
hooks:
	@git config core.hooksPath .githooks
	@chmod +x .githooks/*
	@echo "hooks installed: core.hooksPath -> .githooks"
	@echo "note: 'git push --no-verify' still bypasses them; see .githooks/pre-push"

# And the half that checks the second half happened.
#
# `make hooks` was documented in the help text and asserted by nothing. A clone
# that skipped it ran no pre-push hook at all -- and the pre-push hook is the
# only enforcement point several gates have ever had -- while `make check` went
# green and told the developer everything was wired. A gate whose installation
# is optional and unverified is a gate you find out about from the incident.
#
# Hard failure, not a warning: this session has now corrected four separate
# controls that were written, believed, and enforcing nothing, and a warning
# printed above a green `check: all gates green` is the fifth.
hookcheck:
	@configured=$$(git config --get core.hooksPath 2>/dev/null || true); \
	if [ "$$configured" != ".githooks" ]; then \
		echo "hookcheck: core.hooksPath is $${configured:-unset}, expected .githooks." >&2; \
		echo "           The tracked hooks in .githooks/ are not running. Fix: make hooks" >&2; \
		exit 1; \
	fi; \
	for h in .githooks/*; do \
		if [ ! -x "$$h" ]; then \
			echo "hookcheck: $$h is not executable -- git will skip it. Fix: make hooks" >&2; \
			exit 1; \
		fi; \
	done; \
	echo "hookcheck: core.hooksPath -> .githooks, all hooks executable"

fmt:
	@for m in $(MODULES); do \
		unformatted=$$(cd $$m && gofmt -l .); \
		if [ -n "$$unformatted" ]; then echo "gofmt: $$m:"; echo "$$unformatted"; exit 1; fi; \
	done
	@echo "gofmt: clean"

vet:
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done
# The eval suites are behind `-tags eval` and CI only RUNS them nightly, so an
# untagged vet never compiles them. Both eval files broke on a signature change
# and nothing noticed until the next manual run -- a gate that only fires
# nightly is a gate that reports rot rather than preventing it. Compiling them
# here costs a second and is the cheap half of the check.
	@(cd daemon && go vet -tags eval ./...) || exit 1
# Same reasoning, second tag. `warnscan` gates daemon/warnscan_test.go, the
# offline fire-rate harness that produces the data D5 (warn-mode Design B vs C)
# is waiting on. It was compiled by NOTHING -- not this vet, not any CI job, not
# the pre-push hook -- so it carried exactly the latent-rot risk the eval tags
# above were added to fix, and would have been discovered the next time someone
# needed the decision data rather than at the commit that broke it.
	@(cd daemon && go vet -tags warnscan ./...) || exit 1
	@echo "vet: clean (including -tags eval and -tags warnscan)"

# Compile the windows/ and darwin/ tagged files, which a vet on this machine
# never sees. 23 files carry `//go:build windows` and 3 carry darwin -- the
# peer-credential implementations, the lock and no-follow syscall splits, the
# owner-permission checks. That is security-relevant code on the two platforms
# nobody here can run.
#
# CI's `cross` matrix covered this. When Actions is unavailable -- a spending
# limit, an outage, a fork without minutes -- that cover disappears silently and
# the tree still looks green, because nothing local disagrees. This makes the
# check exist on the machine doing the work.
#
# helper is excluded, and not by oversight: it links onnxruntime_go, which is
# CGO-only, so a cross-target vet reports "build constraints exclude all Go
# files" for reasons that have nothing to do with our code. CI's cross matrix
# omits it too, and builds it natively per-runner instead.
# proxy joined on 2026-09-09. It has no platform-split files, so it cost nothing
# and had been left out for no stated reason -- which is how a module ends up
# outside every cross-platform check by accident rather than by decision.
#
# helper is still absent and CANNOT join: it links onnxruntime_go, which is
# CGO-only, so GOOS=windows reports "build constraints exclude all Go files" for
# reasons unrelated to our code. CI's cross matrix omits it for the same reason.
# That leaves a real blind spot -- a platform-specific symbol in a helper test is
# caught by nothing, anywhere -- recorded as R1.26 rather than papered over with
# a check that appears to cover it.
CROSSVET_MODULES := daemon editapply protocol clients/tui proxy

crossvet:
	@for os in windows darwin; do \
		for m in $(CROSSVET_MODULES); do \
			(cd $$m && GOOS=$$os go vet ./...) || { echo "crossvet: FAILED GOOS=$$os $$m"; exit 1; }; \
		done; \
	done
	@echo "crossvet: clean (windows + darwin, $(words $(CROSSVET_MODULES)) modules)"

# -count=1 DISABLES THE TEST CACHE, and it is not tidiness.
#
# `go test` serves a cached PASS when the inputs look unchanged, and this repo
# has already been bitten: a neuter check reported green because the result came
# from cache rather than from the neutered code. A gate whose answer can predate
# the change it is gating is not a gate.
test:
	@for m in $(MODULES); do (cd $$m && go test -count=1 ./...) || exit 1; done

# -race, not a bare `go test`: the Gate 6 apply/undo locking is only defended by
# the race detector.
race:
	@for m in $(MODULES); do (cd $$m && go test -race -count=1 ./...) || exit 1; done

lint:
	@./scripts/lint.sh

ratchet:
	@./scripts/coverage-ratchet.sh

errcheck:
	@./scripts/errcheck-ceiling.sh

fuzz:
	@./scripts/fuzz.sh

# The real-model retrieval eval. NOT part of `check`, and that is deliberate:
# it downloads an embedding model and indexes the whole repository, so folding
# it into the fast gate would make the fast gate slow enough that people route
# around it. CI runs it on every PR that touches the retrieval path (see
# .github/workflows/retrieval-eval.yml) and weekly regardless.
#
# This target exists so the command is the SAME one locally. On 2026-08-27 a
# chunking change halved real-repo recall and shipped anyway, because `check`
# was green and running the real eval meant remembering a long `go test -tags`
# incantation. One `make eval` is the difference.
eval:
	@(cd daemon && go test -tags eval -count=1 -timeout 40m -v -run 'TestEvalRetrievalQuality|TestRerankEvalRetrievalRanking|TestTokenEfficiencyEval' ./...)

drill:
	@./scripts/sigterm-drill.sh

soak:
	@./scripts/soak.sh

# The one part of the eval suite that belongs in the fast gate: it needs no
# model and no network, scans the repo once, and takes under a second. It fails
# if a committed file contains a retrieval-eval query verbatim -- which is how
# a captured `go test` transcript sat in the indexed corpus from 2026-08-11 to
# 2026-08-28, handing the eval its own answer key. That is a corpus problem, it
# is introduced by committing a file, and `check` is the moment to catch it.
#
# THE OUTPUT IS KEPT ON FAILURE, and the earlier `>/dev/null` is why. On
# 2026-09-15 this gate went red and printed nothing but `make: *** [Makefile:174:
# evalguard] Error 1` -- no file name, no query, no remedy. The diagnostic had to
# be recovered by running the test by hand. The same test in gates.yml runs with
# -v and would have named both offending files in one line, so a developer got a
# strictly worse report than CI from the same assertion: the local/CI divergence
# class this pass has now found four times, in the shape where local runs the
# same check and says less about it.
#
# -v matches gates.yml exactly. Discarded on success so the banner stays one
# line; printed in full on failure, which is the only run where anyone wants it.
evalguard:
	@out="$$(cd daemon && go test -tags eval -count=1 -run 'TestNoIndexedFileEchoesAnEvalQuery' -v ./ 2>&1)" || { printf '%s\n' "$$out" >&2; exit 1; }
	@echo "evalguard: no committed file echoes a retrieval-eval query"

# docs is last and costs ~1s. It is in `check` rather than in a docs-only job
# because a rename breaks links in the same commit that makes it, and that is
# the only moment anyone can fix it cheaply.
check: hookcheck fmt vet crossvet race lint ratchet errcheck evalguard supplychain webview docs debtmarkers parity reach
	@echo "check: all gates green"
	@./scripts/gate-parity.sh --what-ci-adds

# THE GATE ON REACH, and it is deliberately LOCAL-ONLY.
#
# Every other gate here asks "is this work right?" and all of them pass on a
# branch nobody has pushed. This one asks whether the work ARRIVED. Seven
# delivery-gap instances say that question needed an owner.
#
# In `check` and not in CI, and the reason is not cost. A CI runner's clone has
# no local branches at all, so checks 1 and 2 would examine nothing and pass by
# being irrelevant -- the exact failure hookcheck's exclusion names. The person
# who has an undelivered fix is at a terminal, which is where this has to fire.
#
# BLOCKING, not advisory. It was `manual` for one commit, and the tension was
# recorded there rather than hidden: a gate nobody runs is the delivery gap
# applied to the delivery-gap gate. Every one of its ten findings now carries an
# allowlist entry with a reason AND a trigger, so the gate is green on facts
# rather than on silence, and it self-clears -- most triggers are "the remote
# rename" or "the merge lands", and it goes red again the moment either happens
# and the entry is not removed.
reach:
	@./scripts/reach.sh

# Two supply-chain gates. actions-pinned is sub-second and offline;
# govulncheck is ~17s warm and needs the vulnerability database.
#
# In `check` rather than only in CI, for the same reason in both cases: the
# person who adds an unpinned action, or whose toolchain has fallen behind, is
# at a terminal and not reading a workflow log. govulncheck earns its 17s
# against a `check` that already runs the race detector -- and it earns it twice
# over, because the CI job alone was green while this repo's own machine had ten
# reachable stdlib vulnerabilities on the same commit.
supplychain:
	@./scripts/release-signing-guard.sh --self-test
	@./scripts/release-branch-guard.sh --self-test
	@./scripts/actions-pinned.sh
	@./scripts/go-toolchain-pinned.sh --self-test
	@./scripts/go-toolchain-pinned.sh
# Whole-repo supply-chain invariants: `go mod tidy` produces no diff, and no
# shipped binary embeds the path it was built at. Ran ONLY in CI (gates.yml)
# until 2026-09-09, so a developer could not check either before pushing --
# found by scripts/gate-parity.sh on its first run.
	@./scripts/supply-chain.sh
	@./scripts/govulncheck.sh

# The VS Code extension's two offline gates. Node only, no VS Code download, so
# they belong in the fast local gate rather than only in CI -- the person who
# breaks the webview is at a terminal, not reading a workflow log.
#
# webview-check is here because media/main.js, which renders the tool-approval
# panel and the edit-approval flow, was checked by NOTHING until 2026-09-02. The
# first run of it found a ReferenceError that had shipped.
webview:
	@cd clients/vscode && node scripts/verify-vsix.js --self-test
	@cd clients/vscode && node scripts/webview-check.js

# Ran ONLY in CI until 2026-09-09 and had no make target at all, so running it
# before a push required knowing the script path. Found by gate-parity.sh.
debtmarkers:
	@./scripts/debt-markers.sh

# THE GATE ON THE GATES. `make check` and .github/workflows/*.yml are two
# enumerations of what gets checked; this asserts they still agree, and that
# every difference between them is deliberate and carries a reason.
parity:
	@./scripts/gate-parity.sh

docs:
	@./scripts/docs-links.sh
	@./scripts/docs-coderefs.sh
	@./scripts/docs-claims.sh --self-test
	@./scripts/docs-claims.sh
