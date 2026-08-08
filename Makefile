# Developer entry points. CI runs the same scripts, so a green `make check`
# here means the same thing it means there.
#
# There is no root Go module -- only go.work -- so `./...` from the repo root
# fails outright ("directory prefix . does not contain modules listed in
# go.work"). Every target below either names modules explicitly or delegates to
# a script that does. Do not "simplify" any of this to a root-level ./... .

MODULES := daemon editapply proxy helper protocol clients/tui

.PHONY: help hooks test race fmt vet lint ratchet errcheck fuzz check docs drill soak

help:
	@echo "make hooks    install the tracked git hooks (.githooks/) -- do this once"
	@echo "make check    everything CI runs: build, fmt, vet, race, lint, ratchet, docs"
	@echo "make docs     every relative link in a tracked .md resolves"
	@echo "make race     go test -race across all six modules"
	@echo "make lint     staticcheck + ineffassign + bodyclose"
	@echo "make ratchet  per-package coverage floors"
	@echo "make errcheck per-module unchecked-error ceilings (a ratchet, not a gate)"
	@echo "make fuzz     30s per fuzz target (FUZZTIME=5m to search harder)"
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

drill:
	@./scripts/sigterm-drill.sh

soak:
	@./scripts/soak.sh

# docs is last and costs ~1s. It is in `check` rather than in a docs-only job
# because a rename breaks links in the same commit that makes it, and that is
# the only moment anyone can fix it cheaply.
check: fmt vet race lint ratchet errcheck docs
	@echo "check: all gates green"

docs:
	@./scripts/docs-links.sh
