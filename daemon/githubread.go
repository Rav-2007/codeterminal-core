package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// READING GITHUB: issues, pull requests and CI failures, as plain text that a
// person and a model can both read.
//
// EVERYTHING HERE WAS WRITTEN BY SOMEBODY ON GITHUB, and on a public repository
// that is anybody. So every author is shown with their relationship to the
// repository -- an instruction in a comment by "no relationship to the
// repository" is not the owner speaking -- and the text goes to the model only
// inside a fence (githubtools.go) or as attached command output
// (attachedoutput.go), scrubbed and neutralised either way.
//
// BOUNDED, because a busy issue has hundreds of comments and a CI log runs to
// megabytes: what is shown is what answers the question, and it says what it
// left out.

const (
	// githubBodyMax bounds one issue, pull request or comment body.
	githubBodyMax = 4000
	// githubCommentsShown is how many of the latest comments are shown.
	githubCommentsShown = 20
	// githubTextMax bounds everything one read returns.
	githubTextMax = 24 << 10
	// githubListMax is how many issues or pull requests a list shows.
	githubListMax = 30
	// githubLogTextMax bounds the CI failure, which is attached whole: the
	// attached-output limit (attachedoutput.go) is the same 8 KB.
	githubLogTextMax = maxAttachedOutputBytes
	// githubLogsRead is how many failed jobs are read.
	githubLogsRead = 2
)

type ghUser struct {
	Login string `json:"login"`
}

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
	User   ghUser `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Comments          int             `json:"comments"`
	AuthorAssociation string          `json:"author_association"`
	HTMLURL           string          `json:"html_url"`
	PullRequest       json.RawMessage `json:"pull_request"`
}

type ghComment struct {
	User              ghUser `json:"user"`
	Body              string `json:"body"`
	AuthorAssociation string `json:"author_association"`
	CreatedAt         string `json:"created_at"`
}

type ghPull struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
	User   ghUser `json:"user"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
	Head   struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	MergeableState    string `json:"mergeable_state"`
	AuthorAssociation string `json:"author_association"`
	HTMLURL           string `json:"html_url"`
	Additions         int    `json:"additions"`
	Deletions         int    `json:"deletions"`
	ChangedFiles      int    `json:"changed_files"`
}

type ghCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	App        struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

type ghReview struct {
	User  ghUser `json:"user"`
	State string `json:"state"`
}

// githubText is text from GitHub made safe to keep: no control characters
// (a terminal would act on them), no carriage returns, at most max bytes.
func githubText(s string, max int) string {
	s, _ = stripControlCharacters(strings.ReplaceAll(s, "\r\n", "\n"))
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = clipUTF8(s, max) + fmt.Sprintf("\n[… cut: %d more bytes on GitHub]", len(s)-max)
	}
	return s
}

// githubName is a short field from GitHub -- a login, a title, a label -- on
// one line.
func githubName(s string) string {
	s, _ = stripControlCharacters(s)
	return clipRunes(oneLine(s), 200)
}

// githubRelationship is author_association in words.
func githubRelationship(assoc string) string {
	switch strings.ToUpper(assoc) {
	case "OWNER":
		return "owner of the repository"
	case "MEMBER":
		return "member of the organisation that owns it"
	case "COLLABORATOR":
		return "collaborator"
	case "CONTRIBUTOR":
		return "has contributed before"
	case "FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER":
		return "first-time contributor"
	case "NONE", "":
		return "no relationship to the repository"
	}
	return strings.ToLower(githubName(assoc))
}

// githubAuthor is "login (relationship)".
func githubAuthor(u ghUser, assoc string) string {
	return githubName(u.Login) + " (" + githubRelationship(assoc) + ")"
}

// githubCap bounds a whole answer.
func githubCap(s string) string {
	if len(s) <= githubTextMax {
		return s
	}
	return clipUTF8(s, githubTextMax) + "\n[… the rest is left out]"
}

// githubIssueText is one issue and its latest comments. A number that is a
// pull request is shown as one.
func githubIssueText(ctx context.Context, token, repo string, n int) (string, error) {
	var issue ghIssue
	if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, fmt.Sprintf("/issues/%d", n)), nil, &issue); err != nil {
		return "", err
	}
	if len(issue.PullRequest) > 0 && string(issue.PullRequest) != "null" {
		return githubPRText(ctx, token, repo, n)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Issue #%d · %s · %q\n", issue.Number, githubName(issue.State), githubName(issue.Title))
	fmt.Fprintf(&b, "by %s", githubAuthor(issue.User, issue.AuthorAssociation))
	if len(issue.Labels) > 0 {
		names := make([]string, len(issue.Labels))
		for i, l := range issue.Labels {
			names[i] = githubName(l.Name)
		}
		b.WriteString(" · labels: " + strings.Join(names, ", "))
	}
	fmt.Fprintf(&b, " · %d comment(s)\n%s\n", issue.Comments, githubName(issue.HTMLURL))
	if body := githubText(issue.Body, githubBodyMax); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	if issue.Comments > 0 {
		comments, skipped, err := githubLatestComments(ctx, token, repo, n, issue.Comments, githubCommentsShown)
		if err != nil {
			fmt.Fprintf(&b, "\n(the comments could not be read: %v)\n", err)
		} else {
			writeGitHubComments(&b, comments, skipped)
		}
	}
	return githubCap(b.String()), nil
}

// githubLatestComments is the last `want` comments on issue or pull request n,
// which has total of them, and how many earlier ones were left out.
func githubLatestComments(ctx context.Context, token, repo string, n, total, want int) ([]ghComment, int, error) {
	page := (total + 99) / 100
	if page < 1 {
		page = 1
	}
	var comments []ghComment
	path := githubRepoPath(repo, fmt.Sprintf("/issues/%d/comments?per_page=100&page=%d", n, page))
	if _, err := githubCall(ctx, token, http.MethodGet, path, nil, &comments); err != nil {
		return nil, 0, err
	}
	skipped := total - len(comments)
	if len(comments) > want {
		skipped += len(comments) - want
		comments = comments[len(comments)-want:]
	}
	if skipped < 0 {
		skipped = 0
	}
	return comments, skipped, nil
}

func writeGitHubComments(b *strings.Builder, comments []ghComment, skipped int) {
	if skipped > 0 {
		fmt.Fprintf(b, "\n(%d earlier comment(s) not shown)\n", skipped)
	}
	for _, c := range comments {
		fmt.Fprintf(b, "\n— comment by %s, %s:\n%s\n", githubAuthor(c.User, c.AuthorAssociation),
			githubName(strings.SplitN(c.CreatedAt, "T", 2)[0]), githubText(c.Body, githubBodyMax))
	}
}

// githubIssuesText is the open issues -- not pull requests, which GitHub lists
// among them -- narrowed to label when one is given.
func githubIssuesText(ctx context.Context, token, repo, label string) (string, error) {
	query := url.Values{"state": {"open"}, "per_page": {"100"}}
	if label != "" {
		query.Set("labels", label)
	}
	var issues []ghIssue
	if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, "/issues?"+query.Encode()), nil, &issues); err != nil {
		return "", err
	}
	var lines []string
	for _, is := range issues {
		if len(is.PullRequest) > 0 && string(is.PullRequest) != "null" {
			continue
		}
		line := fmt.Sprintf("#%d %s · by %s", is.Number, githubName(is.Title), githubName(is.User.Login))
		if len(is.Labels) > 0 {
			names := make([]string, len(is.Labels))
			for i, l := range is.Labels {
				names[i] = githubName(l.Name)
			}
			line += " · " + strings.Join(names, ", ")
		}
		lines = append(lines, line)
	}
	head := "Open issues in " + repo
	if label != "" {
		head += " labelled " + githubName(label)
	}
	if len(lines) == 0 {
		return head + ": none.", nil
	}
	more := ""
	if len(lines) > githubListMax {
		more = fmt.Sprintf("\n(%d more not shown)", len(lines)-githubListMax)
		lines = lines[:githubListMax]
	}
	return head + ":\n" + strings.Join(lines, "\n") + more, nil
}

// githubPRsText is the open pull requests.
func githubPRsText(ctx context.Context, token, repo string) (string, error) {
	var pulls []ghPull
	path := githubRepoPath(repo, fmt.Sprintf("/pulls?state=open&per_page=%d", githubListMax))
	if _, err := githubCall(ctx, token, http.MethodGet, path, nil, &pulls); err != nil {
		return "", err
	}
	if len(pulls) == 0 {
		return "Open pull requests in " + repo + ": none.", nil
	}
	lines := make([]string, 0, len(pulls))
	for _, p := range pulls {
		draft := ""
		if p.Draft {
			draft = " · draft"
		}
		lines = append(lines, fmt.Sprintf("#%d %s%s · by %s · %s → %s", p.Number, githubName(p.Title), draft,
			githubName(p.User.Login), githubName(p.Head.Ref), githubName(p.Base.Ref)))
	}
	return "Open pull requests in " + repo + ":\n" + strings.Join(lines, "\n") + "\n/pr <number> shows one.", nil
}

// githubPRText is one pull request: who, where, its checks, its reviews,
// whether it can merge, and its description and latest comments.
func githubPRText(ctx context.Context, token, repo string, n int) (string, error) {
	var pr ghPull
	if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, fmt.Sprintf("/pulls/%d", n)), nil, &pr); err != nil {
		return "", err
	}
	state := githubName(pr.State)
	switch {
	case pr.Merged:
		state = "merged"
	case pr.Draft && pr.State == "open":
		state = "open, draft"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request #%d · %s · %q\n", pr.Number, state, githubName(pr.Title))
	fmt.Fprintf(&b, "by %s · %s → %s · %d file(s), +%d −%d\n%s\n", githubAuthor(pr.User, pr.AuthorAssociation),
		githubName(pr.Head.Ref), githubName(pr.Base.Ref), pr.ChangedFiles, pr.Additions, pr.Deletions,
		githubName(pr.HTMLURL))

	if runs, err := githubCheckRuns(ctx, token, repo, pr.Head.SHA); err != nil {
		fmt.Fprintf(&b, "Checks: could not be read (%v)\n", err)
	} else {
		b.WriteString("Checks: " + githubChecksSummary(runs) + "\n")
	}
	var reviews []ghReview
	if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, fmt.Sprintf("/pulls/%d/reviews?per_page=100", n)), nil, &reviews); err != nil {
		fmt.Fprintf(&b, "Reviews: could not be read (%v)\n", err)
	} else {
		b.WriteString("Reviews: " + githubReviewsSummary(reviews) + "\n")
	}
	if pr.State == "open" {
		b.WriteString("Can merge: " + githubMergeableWords(pr.MergeableState) + "\n")
	}
	if body := githubText(pr.Body, githubBodyMax); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	var issue ghIssue // the conversation's comment count lives on the issue
	if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, fmt.Sprintf("/issues/%d", n)), nil, &issue); err == nil && issue.Comments > 0 {
		if comments, skipped, err := githubLatestComments(ctx, token, repo, n, issue.Comments, 10); err == nil {
			writeGitHubComments(&b, comments, skipped)
		}
	}
	return githubCap(b.String()), nil
}

// githubCheckRuns is the latest run of every check on commit sha.
func githubCheckRuns(ctx context.Context, token, repo, sha string) ([]ghCheckRun, error) {
	if !githubSHAPattern.MatchString(sha) {
		return nil, fmt.Errorf("GitHub gave no usable commit for it")
	}
	var answer struct {
		CheckRuns []ghCheckRun `json:"check_runs"`
	}
	path := githubRepoPath(repo, "/commits/"+sha+"/check-runs?filter=latest&per_page=100")
	if _, err := githubCall(ctx, token, http.MethodGet, path, nil, &answer); err != nil {
		return nil, err
	}
	return answer.CheckRuns, nil
}

// githubCheckFailed reports whether a finished check run counts as failed.
func githubCheckFailed(r ghCheckRun) bool {
	switch r.Conclusion {
	case "failure", "timed_out", "startup_failure", "action_required":
		return true
	}
	return false
}

// githubChecksSummary is the runs counted, with the failed ones named.
func githubChecksSummary(runs []ghCheckRun) string {
	if len(runs) == 0 {
		return "none have reported"
	}
	var passed, skipped, cancelled, running int
	var failed []string
	for _, r := range runs {
		switch {
		case r.Status != "completed":
			running++
		case githubCheckFailed(r):
			failed = append(failed, githubName(r.Name))
		case r.Conclusion == "success":
			passed++
		case r.Conclusion == "cancelled":
			cancelled++
		default: // skipped, neutral, stale
			skipped++
		}
	}
	var parts []string
	if len(failed) > 0 {
		sort.Strings(failed)
		parts = append(parts, fmt.Sprintf("%d failed (%s)", len(failed), strings.Join(failed, ", ")))
	}
	for _, c := range []struct {
		n    int
		word string
	}{{passed, "passed"}, {running, "still running"}, {cancelled, "cancelled"}, {skipped, "skipped"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.word))
		}
	}
	return strings.Join(parts, ", ")
}

// githubReviewsSummary is each reviewer's latest verdict.
func githubReviewsSummary(reviews []ghReview) string {
	latest := map[string]string{}
	var order []string
	for _, r := range reviews {
		login := githubName(r.User.Login)
		if r.State == "COMMENTED" && latest[login] != "" {
			continue // a comment does not undo an approval or a change request
		}
		if _, seen := latest[login]; !seen {
			order = append(order, login)
		}
		latest[login] = r.State
	}
	byState := map[string][]string{}
	for _, login := range order {
		byState[latest[login]] = append(byState[latest[login]], login)
	}
	var parts []string
	for _, s := range []struct{ state, words string }{
		{"APPROVED", "approved by"}, {"CHANGES_REQUESTED", "changes requested by"},
		{"COMMENTED", "commented on by"}, {"DISMISSED", "a review dismissed, by"},
	} {
		if names := byState[s.state]; len(names) > 0 {
			parts = append(parts, s.words+" "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		return "none yet"
	}
	return strings.Join(parts, "; ")
}

// githubMergeableWords is mergeable_state in words.
func githubMergeableWords(state string) string {
	switch state {
	case "clean", "has_hooks":
		return "yes"
	case "unstable":
		return "yes, though not every check passed"
	case "blocked":
		return "not yet: the branch rules block it (an approval or a required check is missing)"
	case "behind":
		return "not yet: the branch is behind its base"
	case "dirty":
		return "no: it conflicts with its base"
	case "draft":
		return "not while it is a draft"
	}
	return "GitHub has not worked it out yet"
}

// githubLogText is the failure in pull request n's CI: for each failed
// Actions job, the step that failed and the lines that say why.
func githubLogText(ctx context.Context, token, repo string, n int) (text string, failed bool, err error) {
	var pr ghPull
	if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, fmt.Sprintf("/pulls/%d", n)), nil, &pr); err != nil {
		return "", false, err
	}
	runs, err := githubCheckRuns(ctx, token, repo, pr.Head.SHA)
	if err != nil {
		return "", false, err
	}
	var failedRuns []ghCheckRun
	for _, r := range runs {
		if r.Status == "completed" && githubCheckFailed(r) {
			failedRuns = append(failedRuns, r)
		}
	}
	short := pr.Head.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	if len(failedRuns) == 0 {
		return fmt.Sprintf("No check has failed on pull request #%d (commit %s): %s.", n, short,
			githubChecksSummary(runs)), false, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CI on pull request #%d (commit %s): %s.\n", n, short, githubChecksSummary(runs))
	budget := githubLogTextMax - b.Len()
	read := 0
	for _, r := range failedRuns {
		if r.App.Slug != "github-actions" {
			fmt.Fprintf(&b, "\n✗ %s — not a GitHub Actions job, so its log is not here.\n", githubName(r.Name))
			continue
		}
		if read == githubLogsRead {
			fmt.Fprintf(&b, "\n✗ %s — also failed; /pr %d log shows the first %d only.\n", githubName(r.Name), n, githubLogsRead)
			continue
		}
		read++
		var job struct {
			Steps []struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		}
		step := ""
		if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, fmt.Sprintf("/actions/jobs/%d", r.ID)), nil, &job); err == nil {
			for _, s := range job.Steps {
				if s.Conclusion == "failure" {
					step = githubName(s.Name)
					break
				}
			}
		}
		head := "\n✗ " + githubName(r.Name)
		if step != "" {
			head += fmt.Sprintf(" — failed at the step %q", step)
		}
		b.WriteString(head + "\n")
		log, err := githubJobLog(ctx, token, repo, r.ID)
		if err != nil {
			fmt.Fprintf(&b, "(its log could not be read: %v)\n", err)
			continue
		}
		share := budget / (len(failedRuns) - read + 1)
		if share > budget {
			share = budget
		}
		part := extractCIFailure(log, share-len(head)-2)
		b.WriteString(part + "\n")
		budget -= len(head) + len(part) + 2
	}
	return strings.TrimRight(b.String(), "\n"), true, nil
}

// --- CI logs ---

// githubSHAPattern is a commit id as GitHub gives one; checked before it goes
// into a path.
var githubSHAPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// How much of a CI log's end is kept: a few lines when the lines naming the
// failure were found -- seen in the real client on 2026-10-15, 150 lines of
// "ok" filled the screen under a failure already shown -- and more when
// nothing in the log named it.
const (
	ciTailWhenFound   = 20
	ciTailWhenUnknown = 150
)

var (
	ciTimestamp = regexp.MustCompile(`^\x{FEFF}?\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ?`)
	ciANSI      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// ciSignal is a line that names a failure in the tools this project's CI
	// runs: go test, make, the shell, and GitHub's own error annotation.
	ciSignal = regexp.MustCompile(`--- FAIL|^FAIL\b|^\s+FAIL\b|panic:|^error: |\berror:|\bError:|` +
		`make(?:\[\d+\])?: \*\*\*|fatal:|\bFAILED\b|exit code [1-9]|^FAIL\s`)
)

// extractCIFailure is what in a CI log says why it failed, in at most max
// bytes: the lines that name a failure with a little around each, then the end
// of the log up to GitHub's last error. Timestamps, colour codes and the
// clean-up steps that run after a failure are left out.
func extractCIFailure(raw string, max int) string {
	if max < 200 {
		max = 200
	}
	var lines []string
	lastErr := -1
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = ciANSI.ReplaceAllString(ciTimestamp.ReplaceAllString(line, ""), "")
		switch {
		case strings.HasPrefix(line, "##[endgroup]"), strings.HasPrefix(line, "##[debug]"):
			continue
		case strings.HasPrefix(line, "##[group]"):
			line = "▸ " + strings.TrimPrefix(line, "##[group]")
		case strings.HasPrefix(line, "##[error]"):
			line = "error: " + strings.TrimPrefix(line, "##[error]")
			lastErr = len(lines)
		case strings.HasPrefix(line, "##[warning]"):
			line = "warning: " + strings.TrimPrefix(line, "##[warning]")
		}
		line, _ = stripControlCharacters(line)
		lines = append(lines, line)
	}
	end := len(lines)
	if lastErr >= 0 {
		end = lastErr + 1 // GitHub's last error ends the step; what follows is the job cleaning up
	}
	lines = lines[:end]

	// The lines that name the failure, three before and eight after each.
	keep := make([]bool, len(lines))
	found := false
	for i, line := range lines {
		if ciSignal.MatchString(line) {
			found = true
			for j := max0(i - 3); j < len(lines) && j <= i+8; j++ {
				keep[j] = true
			}
		}
	}
	var signal strings.Builder
	lastShown := -1
	if found {
		gap := false
		for i, k := range keep {
			if !k {
				gap = true
				continue
			}
			if gap && signal.Len() > 0 {
				signal.WriteString("…\n")
			}
			gap = false
			if signal.Len()+len(lines[i])+1 > max/2 {
				signal.WriteString("[… more lines like these]\n")
				break
			}
			signal.WriteString(lines[i] + "\n")
			lastShown = i
		}
	}

	// The end of the log, as much as is left room for: a few lines when the
	// failure was found above, more when nothing in the log named it and the
	// end is all there is to go on.
	room := max - signal.Len() - 80
	keepLines := ciTailWhenUnknown
	if found {
		keepLines = ciTailWhenFound
	}
	var tail []string
	used := 0
	// Only lines the failure section has not shown: none at all, when it
	// already ran to the end.
	for i := len(lines) - 1; i > lastShown && len(tail) < keepLines; i-- {
		if used+len(lines[i])+1 > room {
			break
		}
		tail = append(tail, lines[i])
		used += len(lines[i]) + 1
	}
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}

	var b strings.Builder
	if signal.Len() > 0 {
		b.WriteString("Lines that name the failure:\n" + signal.String() + "\n")
	}
	if len(tail) > 0 {
		b.WriteString("The end of the log:\n" + strings.Join(tail, "\n"))
	}
	return clipUTF8(strings.TrimRight(b.String(), "\n"), max)
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
