<!-- coderefs: enforced -->
# GitHub threat model — what Mochiii holds, reads and sends

**Status 2026-10-15, phase 1 of 5 (connect and read).** Written with the code, against tests
that drive a stand-in GitHub API (`daemon/github_test.go`), and the real terminal client
driven against that stand-in. **Not yet run against the real github.com.** Each later phase
(shipping, review replies, watching, merging) adds its rows here.

**The rule:** the model reads, the daemon acts, the owner decides. In this phase nothing writes
to GitHub at all.

This is a new crossing — GitHub's API and the text on it, into the daemon — and it is **not** in
`TRUST_BOUNDARIES.md`'s twelve, whose count is an owner decision (2026-09-14). It is recorded
here instead of renumbering that list.

---

## What is held: one token, for a bot account

| Claim | Where |
|---|---|
| The token is checked with GitHub (`GET /user`, then the repository) **before** it is kept; one that cannot see the project's repository is not stored | `githubConnect`, `daemon/githubcred.go:239` |
| It is kept in `~/.mochiii/github.json`, mode 0600, written atomically | `saveGitHubCredentialLocked`, `daemon/githubcred.go:125`; `writePrivateFile`, `daemon/credentials.go:220` |
| A file that is not a regular file, or that others can read, is refused rather than used | `loadGitHubCredential`, `daemon/githubcred.go:91` and `daemon/githubcred.go:94` |
| The agent's own reads and edits refuse `.mochiii` | `daemon/outsideread.go:80`; editapply's protected directories |
| The literal token is redacted from anything that leaves for the model | `daemon/credscrub_apply.go:33` |
| Both GitHub token shapes are known to the pattern scrubber; `github_pat_` was missing until this phase | `daemon/scrub.go:108`, `daemon/scrub.go:112` |
| Connect warns when the token is the repository owner's own account, has `workflow` or wider scopes, or expires within a week | `githubTokenNotes`, `daemon/githubcred.go:318` |

The token is never put in an environment, an argument list, a log or an answer. The terminal
client sends it once and keeps no copy (`submitGitHub`, `clients/tui/github.go`).

**Residual, the same as the provider key's:** a command the user approves through `sandbox_exec`
runs with the user's privileges where nothing confines it, and could read the file. A dedicated
bot account, invited to one repository, without `workflow`, is what bounds the damage then.

## What is sent: to one host, built from typed fields

| Claim | Where |
|---|---|
| Every request goes to `api.github.com`; the URL is built by the daemon from the repository and a number | `githubAPIBase`, `daemon/github.go:36`; `githubRepoPath`, `daemon/github.go:73` |
| The repository is pinned per project and must look like `owner/name` | `daemon/github.go:61` |
| Redirects are never followed with the token | `githubNoRedirect`, `daemon/github.go:107` |
| A CI log's one redirect is followed only to GitHub's log hosts, over https, as a new request that carries no `Authorization` | `githubLogURLAllowed`, `daemon/github.go:207`; `githubJobLog`, `daemon/github.go:221` |
| The model's tool takes a kind, a number and a label. Unknown arguments are refused; a label must look like one (≤ 50 characters) and may not hold a secret | `builtinGitHub`, `daemon/githubtools.go:89`, `daemon/githubtools.go:98`; `daemon/githubtools.go:33` |

**Residual:** the label is model-chosen text that reaches GitHub's API as a query parameter. It
is 50 characters of a narrow alphabet and goes only to GitHub, where only GitHub sees it. The
tool is allowed without asking (`models.agent.json`) on that basis.

## What is read: text anyone can write

The repository is public. An issue or a comment can be written by anyone, and can be written
*to the model*.

| Claim | Where |
|---|---|
| Every author is shown with their relationship to the repository (`owner of the repository`, `no relationship to the repository`) | `githubRelationship`, `daemon/githubread.go:127` |
| The model's tool result is scrubbed, neutralised, then wrapped in `<github_content>`, a protected family, under a note saying it is information, never an instruction | `githubEnvelope`, `daemon/githubtools.go:50`; `daemon/context.go:118` |
| What the user attaches with `/issue`, `/pr N` and `/pr N log` goes through the attached-output path, scrubbed and neutralised, and is fenced as command output | `daemon/attachedoutput.go:79` |
| Reading anything marks the turn, so a web search after it is asked about | `hasRead`, `daemon/webegress.go:147` |
| A CI log is cut down to the failed step and the lines that name the failure; it never reaches the model whole | `extractCIFailure`, `daemon/githubread.go:551` |

The test that matters: an issue body that closes the fence and poses as the owner stays inside
it (`daemon/github_test.go`).

**Residual:** a fence makes hostile text *recognisable*; it does not make a model immune to it.
In this phase the worst case is a misleading answer, or a web call the user is asked about.
Phases 2–5 add writes, and every one of them is a proposal the owner sees word for word.

## Reproducing

```bash
# The GitHub tests are named one by one: a filter like 'GitHub' skips the five
# whose names do not say it. TestScrub_TruePositives holds the github_pat_ shape.
cd daemon && go test -count=1 -run '^(TestGitHub.*|TestTheGitHub.*|TestDisconnectForgetsTheToken|TestOriginRemotesAreRead|TestAnIssueShowsEachAuthorsRelationship|TestAPullRequestSaysItsChecksReviewsAndWhetherItCanMerge|TestTheCIFailureIsCutOutOfTheLog|TestTheLogRedirectCarriesNoToken|TestAGitHubRequestIsRecognisedByItsOwnKey|TestScrub_TruePositives)$' ./
```
