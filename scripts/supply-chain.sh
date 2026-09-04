#!/usr/bin/env bash
# Supply-chain invariants that a diff-shaped review cannot see.
#
# Two checks, both cross-file and both silent until they are not:
#
#   TIDY      `go mod tidy` must produce no diff. An untidy go.mod is a
#             dependency graph that does not match the code -- a requirement
#             nothing imports any more, or worse, one the code imports that is
#             recorded only as indirect. Either way the file people audit is not
#             the file the build uses.
#
#   TRIMPATH  A shipped binary must not embed the machine it was built on.
#             MEASURED on this tree before the flag was added: a plain
#             `go build` of clients/tui embedded 678 absolute paths, starting
#             with the developer's home directory and their local Go
#             installation. That is the build host's directory layout and the
#             builder's username, handed to anyone who runs `strings` on a
#             release artifact.
#
# NOT CHECKED HERE, and deliberately: govulncheck. It has its own script and its
# own CI matrix, and it was verified to exit 3 on a reachable finding (measured
# against golang.org/x/text v0.3.0), so `run: govulncheck ./...` already fails
# the build rather than reporting.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
modules="${*:-daemon editapply proxy protocol helper clients/tui}"
status=0
tidied=0

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# ---------------------------------------------------------------- tidy

for m in $modules; do
  dir="$repo_root/$m"
  [ -f "$dir/go.mod" ] || { echo "FAIL  $m — no go.mod"; status=1; continue; }

  before_mod="$(cat "$dir/go.mod")"
  before_sum=""
  [ -f "$dir/go.sum" ] && before_sum="$(cat "$dir/go.sum")"

  out="$( (cd "$dir" && GOWORK=off GOFLAGS=-mod=mod go mod tidy 2>&1) )"
  rc=$?

  after_mod="$(cat "$dir/go.mod")"
  after_sum=""
  [ -f "$dir/go.sum" ] && after_sum="$(cat "$dir/go.sum")"

  # Restore BEFORE reporting, so a failing gate never leaves the tree edited.
  printf '%s\n' "$before_mod" > "$dir/go.mod"
  if [ -n "$before_sum" ]; then printf '%s\n' "$before_sum" > "$dir/go.sum"; fi

  if [ $rc -ne 0 ]; then
    # Fails closed. `go mod tidy` needs every module in the cache or a network;
    # without one it errors, and an error is not a clean tidy.
    echo "FAIL  $m — 'go mod tidy' could not run. This is not a clean result."
    echo "$out" | sed 's/^/      /'
    status=1
    continue
  fi
  if [ "$before_mod" != "$after_mod" ] || [ "$before_sum" != "$after_sum" ]; then
    echo "FAIL  $m — 'go mod tidy' changes go.mod/go.sum. Run it and commit the result."
    diff <(printf '%s\n' "$before_mod") <(printf '%s\n' "$after_mod") | sed 's/^/      /'
    status=1
  else
    echo "ok    $m — go.mod is tidy"
    tidied=$((tidied + 1))
  fi
done

# ------------------------------------------------------------ trimpath

binaries=0
for m in $modules; do
  dir="$repo_root/$m"

  # A MODULE THAT CANNOT BE ASKED WHAT IT IS MUST NOT BE SKIPPED IN SILENCE.
  #
  # The first version of this loop asked `go list` for the package name and
  # `continue`d on anything that was not "main". That treats "this is a library"
  # and "go list failed" as the same answer, so a module with no Go files at all
  # produced a silent skip and the whole gate exited 0 -- measured.
  name="$( (cd "$dir" && go list -f '{{.Name}}' . 2>"$tmp/listerr") )"
  if [ $? -ne 0 ]; then
    echo "FAIL  $m — could not determine the package (go list failed). Not a skip."
    sed 's/^/      /' "$tmp/listerr"
    status=1
    continue
  fi
  # Only a main package produces a binary to inspect. A library is a legitimate
  # nothing-to-do, and is now distinguished from a failure.
  if [ "$name" != "main" ]; then
    continue
  fi
  binaries=$((binaries + 1))
  bin="$tmp/$(echo "$m" | tr / -)"
  if ! (cd "$dir" && go build -trimpath -o "$bin" . 2>"$tmp/err"); then
    echo "FAIL  $m — could not build with -trimpath"
    sed 's/^/      /' "$tmp/err"
    status=1
    continue
  fi

  # The two absolute prefixes that must never survive: where the source lives
  # and where the toolchain lives. HOME covers both on a developer machine and
  # the checkout path covers CI.
  leaked=0
  for prefix in "$repo_root" "${HOME:-/nonexistent-home}" "$(go env GOROOT)" "$(go env GOPATH)"; do
    [ -n "$prefix" ] || continue
    n="$(strings "$bin" | grep -c -- "$prefix" || true)"
    if [ "$n" -gt 0 ]; then
      echo "FAIL  $m — the binary embeds $n reference(s) to $prefix"
      strings "$bin" | grep -- "$prefix" | head -3 | sed 's/^/      /'
      leaked=1
    fi
  done
  if [ "$leaked" -eq 0 ]; then
    echo "ok    $m — builds clean under -trimpath"
  else
    status=1
  fi
done

# INSPECTED-COUNT ASSERTIONS. Both halves of this gate iterate a module list,
# and a list that resolves to nothing would otherwise exit 0 having checked
# nothing at all.
if [ "$tidied" -eq 0 ]; then
  echo "FAIL  no module was checked for tidiness. The module list resolved to nothing."
  status=1
fi
if [ "$binaries" -eq 0 ]; then
  echo "FAIL  no binary was inspected for embedded build paths. Every module in the"
  echo "      list is a library, or none could be identified -- either way this gate"
  echo "      verified nothing about shipped artifacts."
  status=1
fi

if [ "$status" -ne 0 ]; then
  echo
  echo "supply-chain: FAILED"
else
  echo "supply-chain: $tidied module(s) tidy, $binaries binary(ies) clean under -trimpath"
fi
exit "$status"
