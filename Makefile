# Developer entry points. CI runs the same scripts, so a green `make check`
# here means the same thing it means there.
#
# There is no root Go module -- only go.work -- so `./...` from the repo root
# fails outright ("directory prefix . does not contain modules listed in
# go.work"). Every target below either names modules explicitly or delegates to
# a script that does. Do not "simplify" any of this to a root-level ./... .

MODULES := daemon editapply proxy helper protocol clients/tui

.PHONY: help hooks test race fmt vet crossvet lint ratchet errcheck fuzz check docs drill soak eval evalguard

help:
	@echo "make hooks    install the tracked git hooks (.githooks/) -- do this once"
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
CROSSVET_MODULES := daemon editapply protocol clients/tui

crossvet:
	@for os in windows darwin; do \
		for m in $(CROSSVET_MODULES); do \
			(cd $$m && GOOS=$$os go vet ./...) || { echo "crossvet: FAILED GOOS=$$os $$m"; exit 1; }; \
		done; \
	done
	@echo "crossvet: clean (windows + darwin, $(words $(CROSSVET_MODULES)) modules)"

test:
	@for m in $(MODULES); do (cd $$m && go test ./...) || exit 1; done

# -race, not a bare `go test`: the Gate 6 apply/undo locking is only defended by
# the race detector.
race:
	@for m in $(MODULES); do (cd $$m && go test -race ./...) || exit 1; done

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
evalguard:
	@(cd daemon && go test -tags eval -count=1 -run 'TestNoIndexedFileEchoesAnEvalQuery' ./ >/dev/null) || exit 1
	@echo "evalguard: no committed file echoes a retrieval-eval query"

# docs is last and costs ~1s. It is in `check` rather than in a docs-only job
# because a rename breaks links in the same commit that makes it, and that is
# the only moment anyone can fix it cheaply.
check: fmt vet crossvet race lint ratchet errcheck evalguard supplychain docs
	@echo "check: all gates green"

# Sub-second, no network. In `check` rather than only in CI for the reason
# scripts/actions-pinned.sh gives: the person who adds an unpinned action is the
# person who should hear about it, and they are at a terminal, not reading a
# workflow log.
supplychain:
	@./scripts/actions-pinned.sh

docs:
	@./scripts/docs-links.sh
	@./scripts/docs-claims.sh --self-test
	@./scripts/docs-claims.sh
