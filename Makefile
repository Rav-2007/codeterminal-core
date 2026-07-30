# Developer entry points. CI runs the same scripts, so a green `make check`
# here means the same thing it means there.
#
# There is no root Go module -- only go.work -- so `./...` from the repo root
# fails outright ("directory prefix . does not contain modules listed in
# go.work"). Every target below either names modules explicitly or delegates to
# a script that does. Do not "simplify" any of this to a root-level ./... .

MODULES := daemon editapply proxy helper protocol clients/tui

.PHONY: help hooks test race fmt vet lint ratchet fuzz check drill

help:
	@echo "make hooks    install the tracked git hooks (.githooks/) -- do this once"
	@echo "make check    everything CI runs: build, fmt, vet, race, lint, ratchet"
	@echo "make race     go test -race across all six modules"
	@echo "make lint     staticcheck + ineffassign + bodyclose"
	@echo "make ratchet  per-package coverage floors"
	@echo "make fuzz     30s per fuzz target (FUZZTIME=5m to search harder)"
	@echo "make drill    mid-stream SIGTERM drill against the real proxy binary"

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
	@echo "vet: clean"

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

fuzz:
	@./scripts/fuzz.sh

drill:
	@./scripts/sigterm-drill.sh

check: fmt vet race lint ratchet
	@echo "check: all gates green"
