<!-- coderefs: enforced -->
# C3 — the Class III guard: derived, not listed

| | |
|---|---|
| **Branch** | `audit/adversarial-pass`, tree clean |
| **Test commit (red)** | **`76c0849`** — `test(daemon): the Class III guard's list was never checked for completeness` |
| **Fix commit (green)** | **`d519601`** — `fix(daemon): the Class III guard now covers all five handler files` |
| **Step 0** | already closed as a negative result by the owner; **not repeated** |

---

## 1. Derived vs enumerated, at HEAD

| | |
|---|---|
| `builtinToolSurface` enumerated | **2** — `mcpbuiltin.go`, `mcp_ast_edit.go` |
| files declaring `func (s *Server) builtin…` | **5** — the above plus `mcp_exec.go`, `mcp_lsp.go`, `webtools.go` |

`(M)`. The gap was latent, not live: the three uncovered files contain none of the forbidden
readers, so nothing was unbounded. What was missing was the **guard on the guard**.

`TestBuiltinToolSurfaceFilesAllExist` already stops the list naming files that are gone. **Nothing
stopped a file holding a live handler from never being added** — and the list's own comment argues
the hand-list is safe because *"adding a new tool file is a decision someone makes, not something a
pattern silently absorbs."* True, and unenforced: three files had already arrived without the
decision being made.

## 2. The design: derive both sides, and the trap in the obvious derivation

The shape is `gate-parity.sh`'s — derive both sides mechanically, compare, red until someone
decides. Not a hand-list, not a glob.

**The obvious derivation is wrong, and it fails in the direction that looks like success.** Grepping
the registration symbol — `Handler: s.X` — yields four files and **drops `mcp_ast_edit.go`**, a file
the hand-list already gets right. Three handlers are registered through an **inline closure** rather
than a method value:

| registration | `daemon/mcpbuiltin.go` | resolves to |
|---|---|---|
| `Handler: func(ctx, _) { return s.builtinRepoMap(ctx) }` | `:239` | `daemon/mcp_exec.go:199` |
| `Handler: func(ctx, raw) { return s.builtinProposeEdit(ctx, raw, proposals) }` | `:263` | `daemon/mcpbuiltin.go:595` |
| `Handler: func(ctx, raw) { return s.builtinProposeASTEdit(ctx, raw, proposals) }` | `:289` | `daemon/mcp_ast_edit.go:88` |

So a registration-shaped grep would have made the guard **narrower while looking more principled** —
replacing a correct hand-list entry with a derivation that cannot see it. `(M)`

**Resolution:** the test walks the runtime registry (`s.builtinTools(&proposalSink{}, "")`), resolves
each handler with `handlerFuncName`, and for closure-shaped names (`builtinTools.funcN`) follows
`daemonCallGraph`'s edges to the `builtin*` method called. Both helpers already existed in
`daemon/builtincapability_test.go` — `daemonCallGraph` at `:100` already keys func literals the way
the runtime names them, which is exactly what makes this resolvable. **Reused rather than
re-derived** (M8). The one thing added is `daemonFuncDeclFiles`, because that file deliberately
records call edges and not locations.

Four vacuity floors: fewer than 50 parsed declarations, an empty list, no registered tools, and an
empty derived set each **fail** rather than pass.

## 3. The failing test, at HEAD, before the fix `(M)`

```
--- FAIL: TestBuiltinToolSurfaceListIsComplete (0.12s)
    builtinreadalloc_test.go:339: mcp_lsp.go implements a registered built-in handler and is NOT in builtinToolSurface.
        TestBuiltinToolSurfaceBoundsItsReads therefore does not read it, so an unbounded
        os.ReadFile/os.ReadDir added there is not caught by the class guard.
        Add it to the list deliberately, or move the handler.
    builtinreadalloc_test.go:339: mcp_exec.go implements a registered built-in handler and is NOT in builtinToolSurface.
    builtinreadalloc_test.go:339: webtools.go implements a registered built-in handler and is NOT in builtinToolSurface.
FAIL	mochiii/daemon	0.127s
```

Three errors, not four: it did **not** flag `mcp_ast_edit.go`, which is the closure resolution
working.

## 4. The three newly covered files, traced to the end of the chain

C3 requires the trace, not a grep, *"because a handler that applies part of a bound reads exactly
like one that applies all of it."* All three are clean — **and each is clean for a different
reason**, which is the part worth recording.

### `mcp_exec.go` — caps AT SOURCE, and says so
`daemon/mcp_exec.go:41-54` defines `execMaxOutputBytes = 1 << 20` and states it bounds what a command
may produce *"AT SOURCE"*, noting `cmd.CombinedOutput()` is unbounded and that the downstream egress
cap *"bounds what reaches the MODEL"* — **the Class III distinction, written in the code.**
`:297-301` wires it up: `var buf tailBuffer; buf.max = execMaxOutputBytes; cmd.Stdout = &buf;
cmd.Stderr = &buf`. `:321-331` documents `tailBuffer` as *"an io.Writer rather than a post-hoc
truncation so the bytes are never all resident at once — which is the point of capping at source."*
`builtinRepoMap` takes **no model-chosen path** (`:199`, `buildRepoMap(ctx, s.workspace)`), and
`daemon/repomap.go:165` re-runs `shouldSkipFile` before reading — the documented antidote. `(R)` on
the reading, `(M)` on the greps. **Exemplary, not merely clean.**

### `webtools.go` — bounds BEFORE the allocation
`daemon/webtools.go:278` and `:366` both pass `maxWebFetchBytes` into `fetchURL`, and
`daemon/webfetch.go:262` is `io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))`. Its comment
gives the right reason: *"LimitReader, not ContentLength. A Content-Length header is the server's
claim about itself and a hostile server can simply lie."* `(R)` + `(M)`.

### `mcp_lsp.go` — no read of its own, and **listing it is necessary but not sufficient**
`daemon/mcp_lsp.go` contains no `os.Read*`, no `io.ReadAll`, no `make([]byte`. Its handlers' only
outward call is `srv.Call(method, params)` at `:58`. **The unbounded read in that chain is one file
deeper:** `readHeaders`'s `out.ReadString('\n')` at `daemon/lsp_bridge.go:491`, over a
`bufio.NewReader` wrapping a bare `cmd.StdoutPipe()` with no `io.LimitReader` — which is R1.24 / TB7,
already open.

**This is the honest limit of the guard and it belongs in the report, not only in a comment.**
Class III is a property of a **chain**; this guard reads **files**. Adding `mcp_lsp.go` does not
cover the risk `mcp_lsp.go` actually carries. The list's comment now says so at the entry itself.

## 5. Neuter — 3/3 red, restore green `(M)`

C3 asks for two arms. The third exists because it is what justifies §2's design.

| arm | what was changed | result |
|---|---|---|
| baseline | — | `ok mochiii/daemon 0.123s` |
| **1 — a listed file removed** | dropped `"webtools.go"` from the enumeration | **FAIL** — `:362: webtools.go implements a registered built-in handler and is NOT in builtinToolSurface` |
| **2 — a decoy handler file** | new `daemon/zz_decoy_neuter.go` with `builtinDecoyNeuter`, registered in `builtinTools` | **FAIL** — `:363: zz_decoy_neuter.go implements a registered built-in handler and is NOT in builtinToolSurface` |
| **3 — closure resolution disabled** | `if false && strings.Contains(fn, ".")` | **FAIL** — `:371: builtinToolSurface lists mcp_ast_edit.go, but no registered built-in handler resolves to it` |
| restore | `git checkout` + `rm` | `ok mochiii/daemon 0.112s`, tree clean |

**Arm 3 is the measured proof of §2's claim.** With closure resolution off, `repo_map`,
`propose_edit` and `propose_ast_edit` resolve to `builtinTools.func1/2/3`, map to no declaring file,
and `mcp_ast_edit.go` reads as listed-but-underived — i.e. the naive derivation would have argued for
**removing** a correct entry.

**Wider verification after the fix** `(M)`: `go build ./...` ok · `go vet ./...` ok ·
`./scripts/lint.sh daemon` exit 0 (staticcheck, ineffassign, bodyclose) · full `go test -count=1 .`
**ok, 109.381s** WALL-CLOCK.

## 6. Does this close the class, or only this instance?

**It closes one class and explicitly does not close the other. Both halves matter.**

- **CLOSED: "a handler file can be absent from the guard's list without anything noticing."** The
  list is now checked against a derived set in both directions, on every run, with four vacuity
  floors and three neuter arms. A new handler file is red until someone decides about it — which is
  what the list's comment always claimed and never enforced.
- **NOT CLOSED: Class III itself.** Class III is *a cap applied to the result after the whole
  resource has been materialised*, and that is a property of a call chain. This guard is file-scoped:
  it greps `os.ReadFile(`, `os.ReadDir(`, `ReadDir(-1)` in five files with comments stripped. An
  unbounded read reached **through** a listed file — exactly `mcp_lsp.go` → `lsp_bridge.go` — is
  invisible to it. **The instance count for Class III is still zero in the covered files, and the
  known live one (R1.24 / TB7) is outside any file this guard reads.**

Closing the second would mean a reachability guard over the call graph rather than a file grep. The
material already exists — `daemonCallGraph` + `reachesSpawn` in `builtincapability_test.go` do
precisely that shape for `exec.Command`. **An analogous `reachesUnboundedRead` is the obvious next
step and is not built here.** That is a scope decision, not an oversight; recorded for the handover
list.

## 7. The C1d carry-in: is this guard environment-dependent?

C1d flagged that `docs-coderefs`'s ambiguity check reads local working state — `.mochiii/backups/`
makes a bare basename resolve to 1 file in CI and 3 on a developer machine — and asked whether the
Class III guard has the same property.

**It does not, and the distinction is worth keeping separate.** `(M)` This guard's inputs are
`os.ReadDir(".")` over the package's own non-test `.go` files plus the runtime registry from
`s.builtinTools(...)`. Both **are** the code under test. There is no dependence on the operator's
working directory, no `find .` over sibling trees, and no untracked state in scope.

**But it has a different defect of the same family: scope-vs-property mismatch.** `docs-coderefs`
answers a question about the code using something that is not only the code. This guard answers a
question about a **chain** using something that is only a **file**. Neither is wrong about what it
measures; both are narrower than the claim their name suggests. **New M5 pair: *the guard reads the
right code* ≠ *the guard reads enough of it*.**

## 8. Premises that did not hold (H4)

1. **"Grep the registration symbol, not filenames."** Sound in principle and insufficient here: the
   registration symbol misses three closure-wrapped handlers and would have dropped `mcp_ast_edit.go`.
   The derivation had to go through a call graph. §2, proved by neuter arm 3.
2. **"The three uncovered files are clean" implies the guard now covers their risk.** `mcp_lsp.go` is
   clean and its chain is not. §4, §6.
3. **"Reuse `gate-parity.sh`'s shape."** Adopted — but the reusable *code* turned out to be in
   `builtincapability_test.go`, not in the shell gate. `gate-parity.sh` supplied the design; the Go
   test supplied the machinery.

## 9. Self-corrections (H3)

1. **A real mistake in the neuter run, caught by its own restore check.** I neutered before
   committing the fix, so arm 1's `git checkout` discarded the **uncommitted** extension. Arms 2 and
   3 then ran against the pre-fix list — their results were still valid (both named the decoy and the
   `mcp_ast_edit.go` regression correctly) but the "RESTORED" line read `FAIL`, which is what
   exposed it. Re-applied, committed as `d519601`, and re-ran all three arms against the committed
   fix: 3/3 red, restore `ok`. **The restore check earned its place** — without it I would have
   reported 3/3 from a tree that no longer held the fix.
2. **Shrunk before reporting.** I first wrote that the three uncovered files were "clean" on the
   strength of the recon pass's four-spelling grep. That is not a trace. The reads in §4 are what
   support the claim, and one of them changed the conclusion for `mcp_lsp.go` from "clean" to "clean
   at file level, and that is not the same thing."

## 10. What I did not verify

1. **I did not build `reachesUnboundedRead`.** §6. The chain-level Class III question stays open, and
   R1.24 / TB7 remains the known live instance outside this guard's reach.
2. **The forbidden-reader list is unchanged** — `os.ReadFile(`, `os.ReadDir(`, `ReadDir(-1)`. I did
   not test whether other unbounded spellings should join it (`io.ReadAll` without a limiter,
   `bufio.Scanner` with a raised buffer, `ReadString`). `(U)`: `io.ReadAll` appears in daemon product
   code six times and **every one is wrapped in `io.LimitReader`** except
   `daemon/apply_cmd.go:67`'s `os.Stdin` read, which is a CLI path taking the user's own bytes. Adding
   `io.ReadAll(` to `unboundedReaders` would therefore need a limiter-aware exception, which is why I
   did not.
3. **I did not run the other five modules' tests**, only `daemon`. Nothing outside `daemon/` changed.
4. **I did not run `make check`.** build, vet, `lint.sh daemon` and the full daemon suite were run.
5. **`webtools.go`'s `maxWebFetchBytes` value was not read** — only that it is passed in and used as a
   `LimitReader` bound. Whether the number is *small enough* is a different question from whether the
   bound is applied before allocation. `(U)`.
6. **The decoy in arm 2 was registered, not exercised.** It proved the derivation sees a new file; it
   did not prove the read-bounding guard would then inspect it — though arm 1, which removes a real
   file, covers the same edge from the other side.
7. **Chains traced to the end:** registration → `handlerFuncName` → closure edges → declaring files.
   `mcp_exec.go` → `execMaxOutputBytes` → `tailBuffer` as an `io.Writer` → `builtinRepoMap` →
   `buildRepoMap` → `shouldSkipFile`. `webtools.go` → `fetchURL` → `io.LimitReader`. `mcp_lsp.go` →
   `srv.Call` → `lsp_bridge.go`'s `readHeaders`. **Not traced:** `srv.Call`'s own request path;
   `buildRepoMap`'s full walk beyond its eligibility gate; whether `bufio` buffer sizes anywhere in
   `daemon/` constitute a fourth unbounded spelling.

## 11. Recipients

| What | Of whom | Time |
|---|---|---|
| **Decide whether a chain-level `reachesUnboundedRead` guard is worth building.** `daemonCallGraph` + `reachesSpawn` are the template; it would cover R1.24/TB7, which no file-scoped guard can | repo owner | 20 min to decide |
| Whether `io.ReadAll(` joins `unboundedReaders` with a limiter-aware exception | `daemon/` owner | 15 min |
| R1.24 / TB7 itself — the unbounded LSP header read — is unchanged by this chunk and still open | `daemon/` owner | per the register |

Nobody above has been contacted.
