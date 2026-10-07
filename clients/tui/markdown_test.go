package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// Tests for markdown.go: a model's answer drawn as markdown, in blue and grey.
//
// Everything here runs under the colourless profile TestMain pins unless it
// says otherwise, so an expected string is the characters on screen and
// nothing else.

// answerSample is an answer shaped like the one the owner asked for
// (2026-10-06): a paragraph, a table, a rule, a heading, a numbered list with
// its key phrases marked, code, a link, a quote.
const answerSample = `AI is the broad goal; **machine learning** is the part where the system *learns from data*.

## Skills to build

| Core today | Emerging |
|---|---|
| MLOps tools (MLflow, Kubeflow, Vertex AI, Weights & Biases) | AI governance / audit tooling |
| Cloud & containers (K8s, Docker, Terraform) | Edge runtimes (ONNX Runtime, TFLite, TVM) |
| Statistics / probability, optimization | Causality, counterfactual reasoning |

---

### How to Position Yourself

1. **Pick a "vertical" + "horizontal" combo** - e.g., *LLM fine-tuning for healthcare* or *Edge ML for autonomous drones*.
2. **Contribute to open-source** - libraries (Hugging Face, PyTorch Lightning, vLLM) give visible proof of skill.
3. **Build end-to-end demos** - data → model → serving → monitoring → UI.

Install with ` + "`pip install scikit-learn`" + `, then:

` + "```python" + `
from sklearn.linear_model import LinearRegression

model = LinearRegression().fit(X_train, y_train)
print(model.score(X_test, y_test))  # 5*3 = 15, **not bold**, snake_case stays
` + "```" + `

- Start with [the tutorial](https://scikit-learn.org/stable/tutorial/)
- [x] Python basics
- [ ] Linear algebra
  - nested, with 日本語 and an emoji 🎉

> **Note:** the field moves fast; ~~memorise APIs~~ learn the ideas.
`

func TestAnAnswersMarkdownIsDrawnNotPrinted(t *testing.T) {
	cases := []struct {
		name, in string
		width    int
		want     string
	}{
		{"strong and emphasis lose their marks", "This is **important** and *subtle*, ***both***.", 80,
			"Mochiii: This is important and subtle, both."},
		{"a heading that opens the answer sits under the label", "## Skills\ntext", 80,
			"Mochiii:\nSkills\ntext"},
		{"a heading stands apart from what is above it", "intro\n## Skills\nmore", 80,
			"Mochiii: intro\n\nSkills\nmore"},
		{"a closing run of hashes is not part of the title", "# Title ##", 80,
			"Mochiii:\nTitle"},
		{"every bullet character is one bullet", "Steps:\n- one\n* two\n+ three", 80,
			"Mochiii: Steps:\n• one\n• two\n• three"},
		{"a number is drawn as written", "7. seventh\n8) eighth", 80,
			"Mochiii:\n7. seventh\n8) eighth"},
		{"a nested item keeps its indent", "- a\n  - b\n    1. c", 80,
			"Mochiii:\n• a\n  • b\n    1. c"},
		{"an item that wraps hangs under its text", "- one two three four five six", 14,
			"Mochiii:\n• one two\n  three four\n  five six"},
		{"code loses its backticks and nothing else", "run `go test ./...` and ``a ` b`` now", 80,
			"Mochiii: run go test ./... and a ` b now"},
		{"a link shows where it goes", "see [the docs](https://x.dev/a \"title\") here", 80,
			"Mochiii: see the docs (https://x.dev/a) here"},
		{"an address that is its own text is shown once", "[https://x.dev](https://x.dev) and <https://y.dev>", 80,
			"Mochiii: https://x.dev and https://y.dev"},
		{"an image is its description and its address", "![a chart](img/c.png)", 80,
			"Mochiii: a chart (img/c.png)"},
		{"a rule is a line across the width", "a\n\n---\n\nb", 12,
			"Mochiii: a\n\n────────────\n\nb"},
		{"a quote has a bar", "> quoted **text**\n>> deeper", 80,
			"Mochiii:\n│ quoted text\n│ │ deeper"},
		{"a task list shows its boxes", "- [x] done\n- [ ] todo", 80,
			"Mochiii:\n☑ done\n☐ todo"},
		{"a break and an entity", "a &amp; b<br>c &lt;d&gt;", 80,
			"Mochiii: a & b\nc <d>"},
		{"struck text", "~~old~~ new", 80,
			"Mochiii: old new"},
		{"an escaped mark is the mark", `\*not emphasis\* and \# not a heading`, 80,
			"Mochiii: *not emphasis* and # not a heading"},
		{"emphasis nests", "*a **b** c* and **d *e* f**", 80,
			"Mochiii: a b c and d e f"},
		{"emphasis works without spaces to stand on", "这是**重点**内容", 80,
			"Mochiii: 这是重点内容"},
		{"blank lines collapse to one, and none lead or trail", "\n\na\n\n\n\nb\n\n\n", 80,
			"Mochiii: a\n\nb"},
		{"a paragraph's own line breaks are kept", "step one\nstep two", 80,
			"Mochiii: step one\nstep two"},
		{"an indented line keeps its indent, and so do its wraps", "x\n    one two three four", 14,
			"Mochiii: x\n    one two\n    three four"},
		{"nothing at all is still the label", "", 80,
			"Mochiii: "},
		{"nothing but blank lines is still the label", "\n \n", 80,
			"Mochiii: "},
	}
	for _, c := range cases {
		if got := renderAnswer(c.in, c.width); got != c.want {
			t.Errorf("%s\n   in: %q\n  got: %q\n want: %q", c.name, c.in, got, c.want)
		}
	}
}

// THE OTHER HALF, AND THE HALF THAT MATTERS MORE FOR A CODING ASSISTANT: text
// that looks like markdown to a parser and is not. Each of these lost
// characters under plain CommonMark rules. A star or an underscore that
// vanishes from a line of prose about code changes what the line says.
func TestWhatIsNotMarkdownIsLeftAsWritten(t *testing.T) {
	for _, in := range []string{
		"5*3 = 15 and 2*4 = 8",
		"2**10 is 1024 and 3**2 is 9",
		"a * b * c",
		"the snake_case_name and other_name here",
		"def f(*args, **kwargs):",
		"the __init__ method checks __name__ against __my_var__",
		"#hashtag is not a heading",
		"#!/bin/sh",
		"3.14 is pi",
		"-v is a flag and --verbose is too",
		"use *.go files",
		"handlers[0](req) and m[key](a.b)",
		"[not a link] and [ref][1] and [x](y)",
		"a < b && c > d",
		"x | y",
		"about ~5 items, or ~~~",
		`C:\Users\name\file.txt`,
		"**unclosed strong",
		"`unclosed code",
		"*unclosed emphasis",
		"an_ underscore_ and a _ lone one",
		"AT&T and R&D; and &unknown;",
		"<div> and <notatag and a<b>c",
		"- ",
		"1.",
		"=======",
		"price: $5_000 to $10_000",
	} {
		want := assistantLabel + in
		if in == "- " {
			want = assistantLabel + "-" // a trailing space is not content
		}
		if got := renderAnswer(in, 200); got != want {
			t.Errorf("changed text that is not markdown:\n   in: %q\n  got: %q", in, got)
		}
	}
}

// A CODE BLOCK IS WHAT GETS COPIED OUT OF THE TERMINAL, and an edit block is
// what the reader checks against the file. Both are drawn character for
// character: every line inside them is a line of the output, whatever it
// looks like to a markdown parser.
func TestCodeAndEditBlocksAreDrawnCharacterForCharacter(t *testing.T) {
	code := []string{
		"# not a heading",
		"- not a bullet",
		"**not strong** and _not emphasis_ and `not code`",
		"| not | a | table |",
		"|-----|---|-------|",
		"> not a quote",
		"---",
		"        deeply   spaced   out",
		"",
		"after a blank line, which is content here",
		"[not](a.link) &amp; <br>",
	}
	for _, fence := range []string{"```", "~~~", "````"} {
		in := "before\n\n" + fence + "go\n" + strings.Join(code, "\n") + "\n" + fence + "\n\n**after**"
		want := "Mochiii: before\n\n" + fence + "go\n" + strings.Join(code, "\n") + "\n" + fence + "\n\nafter"
		if got := renderAnswer(in, 200); got != want {
			t.Errorf("a %s fence was not drawn as written:\n  got: %q\n want: %q", fence, got, want)
		}
	}

	// A tab is four spaces, as it was when Lip Gloss drew the answer.
	if got := renderAnswer("```\nif x {\n\treturn\n}\n```", 80); got != "Mochiii:\n```\nif x {\n    return\n}\n```" {
		t.Errorf("a tab inside code: %q", got)
	}
	// A fence still open (the answer is still arriving) is code to the end.
	if got := renderAnswer("```\n**a**\n# b", 80); got != "Mochiii:\n```\n**a**\n# b" {
		t.Errorf("an unclosed fence: %q", got)
	}
	// A fence under a list item is indented, by whatever the model chose.
	if got := renderAnswer("1. run it:\n   ```sh\n   make **all**\n   ```", 80); got != "Mochiii:\n1. run it:\n   ```sh\n   make **all**\n   ```" {
		t.Errorf("an indented fence: %q", got)
	}
	// "```x```" on one line is inline code, not a fence that never closes.
	if got := renderAnswer("```x```\n**b**", 80); got != "Mochiii: x\nb" {
		t.Errorf("a one-line triple backtick: %q", got)
	}

	edit := "path: daemon/x.go\n<<<<<<< SEARCH\n# old **line**\n- item\n=======\n# new _line_\n\n| a | b |\n>>>>>>> REPLACE"
	if got := renderAnswer("**Here**:\n\n"+edit+"\n\n**done**", 200); got != "Mochiii: Here:\n\n"+edit+"\n\ndone" {
		t.Errorf("an edit block was not drawn as written:\n  got: %q", got)
	}
	// Still arriving: everything after the SEARCH marker is the block.
	if got := renderAnswer("<<<<<<< SEARCH\n**a**\n=======\n**b**", 80); got != "Mochiii:\n<<<<<<< SEARCH\n**a**\n=======\n**b**" {
		t.Errorf("an unfinished edit block: %q", got)
	}
}

func TestATableIsBoxedToFitTheWidth(t *testing.T) {
	const small = "| Name | Qty | Note |\n|:--|--:|:-:|\n| apple | 3 | ok |\n| kiwi | 12 | **best** |"
	want := strings.Join([]string{
		"Mochiii:",
		"┌───────┬─────┬──────┐",
		"│ Name  │ Qty │ Note │",
		"├───────┼─────┼──────┤",
		"│ apple │   3 │  ok  │",
		"├───────┼─────┼──────┤",
		"│ kiwi  │  12 │ best │",
		"└───────┴─────┴──────┘",
	}, "\n")
	if got := renderAnswer(small, 80); got != want {
		t.Errorf("a small table:\n got:\n%s\nwant:\n%s", got, want)
	}

	// One long column is the one that wraps; the short ones beside it keep
	// their width.
	const wide = "| Key | Meaning |\n|---|---|\n| a | one two three four five six seven eight nine ten |\n| bb | x |"
	want = strings.Join([]string{
		"Mochiii:",
		"┌─────┬──────────────────────┐",
		"│ Key │ Meaning              │",
		"├─────┼──────────────────────┤",
		"│ a   │ one two three four   │",
		"│     │ five six seven eight │",
		"│     │ nine ten             │",
		"├─────┼──────────────────────┤",
		"│ bb  │ x                    │",
		"└─────┴──────────────────────┘",
	}, "\n")
	if got := renderAnswer(wide, 30); got != want {
		t.Errorf("a table with one long column at width 30:\n got:\n%s\nwant:\n%s", got, want)
	}

	// A break inside a cell, a short row and a long one.
	const ragged = "| a | b |\n|---|---|\n| one<br>two |\n| x | y | dropped |"
	want = strings.Join([]string{
		"Mochiii:",
		"┌─────┬───┐",
		"│ a   │ b │",
		"├─────┼───┤",
		"│ one │   │",
		"│ two │   │",
		"├─────┼───┤",
		"│ x   │ y │",
		"└─────┴───┘",
	}, "\n")
	if got := renderAnswer(ragged, 80); got != want {
		t.Errorf("a ragged table:\n got:\n%s\nwant:\n%s", got, want)
	}

	// Too narrow to box: each row as "header: value", nothing dropped.
	want = "Mochiii:\nName: apple\nQty: 3\nNote: ok\n\nName: kiwi\nQty: 12\nNote: best"
	if got := renderAnswer(small, 14); got != want {
		t.Errorf("a table at width 14 should be listed by row:\n got: %q\nwant: %q", got, want)
	}

	// What is not a table stays text.
	for _, in := range []string{
		"| a | b |",                // a header with nothing under it (the next row has not arrived)
		"| a | b |\n| c | d |",     // no delimiter row
		"a | b\n---",               // a rule under a line that has a pipe
		"| a | b |\n|---|---|---|", // a delimiter row for a different table
	} {
		if got := renderAnswer(in, 80); strings.ContainsAny(got, "┌├└") {
			t.Errorf("%q was boxed as a table:\n%s", in, got)
		}
	}
}

// answerCorpus is what the properties below are checked over: the sample, each
// shape on its own, and the awkward text the wrap tests already know about.
func answerCorpus() []string {
	corpus := []string{
		answerSample,
		"plain words with nothing marked in them at all, long enough to wrap more than once at most widths",
		"**" + strings.Repeat("strong words ", 30) + "**",
		"- " + strings.Repeat("x", 300),
		"1. see https://example.com/" + strings.Repeat("a/", 60) + " and *then* stop",
		"> " + strings.Repeat("quoted *words* ", 20),
		"# " + strings.Repeat("a long heading ", 12),
		"| a | b | c | d | e | f |\n|---|---|---|---|---|---|\n| 1 | 2 | 3 | 4 | 5 | " + strings.Repeat("six ", 30) + "|",
		"| 日本語 | 🎉 emoji |\n|---|---|\n| e\u0301 and 👩‍👩‍👧‍👦 | " + strings.Repeat("日本語", 20) + " |",
		"```\n" + strings.Repeat("code ", 60) + "\n\t\tindented\n```",
		"                                                  very far in",
		"日本語のテキスト " + strings.Repeat("日本語", 40),
		"a **b\nc** d *e\nf* `g\nh`",
		"- a\n\n  continued under the item\n\n- b\n   1. c\n      - d",
	}
	for _, f := range textFragments {
		corpus = append(corpus, f, "**"+f+"**", "- "+f, "| "+f+" | x |\n|---|---|\n| y | "+f+" |")
	}
	return corpus
}

var sgrSequence = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// leavesColourOpen reports whether line ends with a colour or weight still
// set: its last SGR sequence is something other than a reset.
func leavesColourOpen(line string) bool {
	open := false
	for _, m := range sgrSequence.FindAllStringSubmatch(line, -1) {
		open = m[1] != "0" && m[1] != ""
	}
	return open
}

// THE PROPERTIES THE REST OF THE CLIENT RELIES ON, over every document in the
// corpus at every width a terminal can have.
func TestAnAnswerFitsItsWidthAndEveryLineClosesItsColour(t *testing.T) {
	for _, doc := range answerCorpus() {
		for width := 0; width <= 132; width++ {
			plain := renderAnswer(doc, width)
			var coloured string
			withColorProfile(t, termenv.ANSI256, func() { coloured = renderAnswer(doc, width) })

			// COLOUR CHANGES NO CHARACTER. Where the escape sequences go must
			// not depend on anything but the pens.
			if stripped := ansi.Strip(coloured); stripped != plain {
				t.Fatalf("width %d: the coloured answer is not the plain one with colour added\n  doc: %q\nplain: %q\n  got: %q",
					width, clipForTest(doc), plain, stripped)
			}

			for n, line := range strings.Split(coloured, "\n") {
				// NO COLOUR CROSSES A NEWLINE: the viewport shows a window of
				// lines and Bubble Tea redraws only the ones that changed.
				if leavesColourOpen(line) {
					t.Fatalf("width %d, line %d leaves a colour open: %q\n  doc: %q", width, n, line, clipForTest(doc))
				}
				// NO LINE IS WIDER THAN THE TERMINAL. Asserted from 24 columns
				// up; below that a list's own marker and a wide character can
				// each be wider than what is left, and wrapToWidth, which runs
				// after this, is what keeps the screen whole.
				if width >= 24 {
					if w := lipgloss.Width(line); w > width {
						t.Fatalf("width %d, line %d is %d columns: %q\n  doc: %q", width, n, w, line, clipForTest(doc))
					}
				}
			}

			// wrapToWidth runs over everything the viewport is given. It must
			// find nothing to do here: a line it re-wrapped would be a line
			// whose second half has lost its colour.
			if width >= 24 {
				if again := wrapToWidth(coloured, width); again != coloured {
					t.Fatalf("width %d: wrapToWidth changed an answer that was already laid out\n  doc: %q", width, clipForTest(doc))
				}
			}
		}
	}
}

// NOTHING IS LOST, AND NO ORDINARY WORD IS BROKEN. From 24 columns up, every
// word of the answer up to mdWordFloor columns long is on screen whole -- in a
// paragraph, a list and a table cell alike. A longer one (a URL, a line of
// code) may be broken across lines, with every character still there in order.
func TestNoWordOfAnAnswerIsLostAtAnyWidth(t *testing.T) {
	words := strings.Fields("AI broad goal machine learning learns data Skills build Core today Emerging MLOps Kubeflow " +
		"governance audit tooling Terraform Runtime Causality counterfactual Position Yourself vertical horizontal " +
		"healthcare drones Contribute libraries PyTorch demos monitoring Install scikit-learn sklearn.linear_model " +
		"LinearRegression().fit(X_train, snake_case tutorial https://scikit-learn.org/stable/tutorial/ Python basics " +
		"Linear algebra nested 日本語 🎉 Note: field memorise APIs ideas.")
	whole, long := 0, 0
	for width := 24; width <= 132; width++ {
		out := renderAnswer(answerSample, width)
		spaced := strings.Join(strings.Fields(out), " ")
		squashed := strings.Join(strings.Fields(out), "")
		for _, w := range words {
			if lipgloss.Width(w) <= mdWordFloor {
				whole++
				if !strings.Contains(spaced, w) {
					t.Fatalf("width %d: %q is not on screen in one piece", width, w)
				}
				continue
			}
			long++
			if !strings.Contains(squashed, w) {
				t.Fatalf("width %d: the characters of %q are not all on screen in order", width, w)
			}
		}
	}
	if whole == 0 || long == 0 {
		t.Fatalf("checked %d short words and %d long ones; one of the two cases was never exercised", whole, long)
	}
	// Narrower than that any word may be broken, but its letters are all
	// still there, in order.
	for width := 1; width < 24; width++ {
		got := strings.Join(strings.Fields(renderAnswer("**Kubeflow** and `Terraform`", width)), "")
		if !strings.Contains(got, "Kubeflow") || !strings.Contains(got, "Terraform") {
			t.Fatalf("width %d: letters were lost: %q", width, got)
		}
	}
}

// AN ANSWER ARRIVES A TOKEN AT A TIME, and is drawn after each one. Every
// prefix of an answer -- half a table, an unclosed "**", a fence with no end --
// has to draw, inside the width, with every colour closed.
func TestEveryPrefixOfAnAnswerDraws(t *testing.T) {
	const width = 60
	withColorProfile(t, termenv.ANSI256, func() {
		for i := range answerSample {
			if !utf8.RuneStart(answerSample[i]) {
				continue
			}
			out := renderAnswer(answerSample[:i], width)
			for n, line := range strings.Split(out, "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("after %d bytes, line %d is %d columns: %q", i, n, w, line)
				}
				if leavesColourOpen(line) {
					t.Fatalf("after %d bytes, line %d leaves a colour open: %q", i, n, line)
				}
			}
		}
	})
}

// THE PALETTE THE OWNER ASKED FOR: "use those two colors (blue, grey)". An
// answer is drawn in the two blues and the greys of styles.go and in nothing
// else, and what the model marked as mattering is the blue.
func TestAnAnswerIsDrawnInBlueAndGreyOnly(t *testing.T) {
	palette := map[string]string{}
	for _, c := range []lipgloss.Color{colorBlue, colorSteel, colorGray, colorDim, colorChip} {
		palette[string(c)] = ""
	}
	colour := regexp.MustCompile(`[34]8;5;(\d+)`)

	withColorProfile(t, termenv.ANSI256, func() {
		out := renderAnswer(answerSample, 100)
		seen := map[string]bool{}
		for _, m := range colour.FindAllStringSubmatch(out, -1) {
			seen[m[1]] = true
			if _, ok := palette[m[1]]; !ok {
				t.Errorf("colour %s is in an answer and is not blue or grey (styles.go)", m[1])
			}
		}
		// ANTI-VACUITY: the sample really exercises both blues and both greys.
		for c := range palette {
			if c != string(colorChip) && !seen[c] {
				t.Errorf("colour %s is in the palette and the sample never used it; the test would not notice it changing", c)
			}
		}

		for _, c := range []struct {
			what, in, text string
			color          lipgloss.Color
			attr           string
		}{
			{"**strong**", "a **key point** here", "key point", colorBlue, "1"},
			{"a heading", "## Title", "Title", colorBlue, "1"},
			{"a table's header", "| Head | b |\n|---|---|\n| c | d |", "Head", colorBlue, "1"},
			{"*emphasis*", "a *soft point* here", "soft point", colorSteel, "3"},
			{"`code`", "run `make check` now", "make check", colorSteel, ""},
			{"the answer's own words", "ordinary words", "ordinary words", colorGray, ""},
		} {
			out := renderAnswer(c.in, 80)
			i := strings.Index(out, c.text)
			if i < 0 {
				t.Fatalf("%s: %q is not in %q", c.what, c.text, out)
			}
			before := out[:i]
			sgr := before[strings.LastIndex(before, "\x1b["):]
			if !strings.Contains(sgr, "38;5;"+string(c.color)) {
				t.Errorf("%s is drawn with %q, want colour %s", c.what, sgr, c.color)
			}
			params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sgr, "\x1b["), "m"), ";")
			if c.attr != "" && params[0] != c.attr {
				t.Errorf("%s is drawn with %q, want attribute %s (bold is 1, italic is 3)", c.what, sgr, c.attr)
			}
		}
	})

	// NO_COLOR is honoured: not one escape sequence.
	if out := renderAnswer(answerSample, 100); strings.Contains(out, "\x1b") {
		t.Errorf("an answer under the colourless profile carries escape sequences: %q", clipForTest(out))
	}
	// And a 16-colour terminal is given 16-colour codes, not 256-colour ones.
	withColorProfile(t, termenv.ANSI, func() {
		if out := renderAnswer(answerSample, 100); strings.Contains(out, "38;5;") || !strings.Contains(out, "\x1b[") {
			t.Errorf("an answer under the 16-colour profile: %q", clipForTest(out))
		}
	})
}

// ONLY A MODEL WRITES MARKDOWN. What this client writes itself -- /help's
// aligned columns, /git's "## branch" line, a diff's leading "-" -- is drawn
// exactly as it always was.
func TestACommandsOutputIsNotDrawnAsMarkdown(t *testing.T) {
	const text = "## main...origin/main\n M file_one.go\n- removed **line**\n| a | b |\n|---|---|"
	old := assistantStyle.Render(assistantLabel + text)
	for _, tn := range []turn{
		{role: roleAssistant, text: text, local: true},
		{role: roleAssistant, text: text, verbatim: true},
		{role: roleAssistant, text: text, local: true, verbatim: true},
	} {
		if got := renderTurnBlock(tn, 80); got != old {
			t.Errorf("a command's output (local=%v verbatim=%v) was redrawn:\n got: %q\nwant: %q", tn.local, tn.verbatim, got, old)
		}
	}
	// ANTI-VACUITY: the same text from a model IS drawn differently.
	if got := renderTurnBlock(turn{role: roleAssistant, text: text}, 80); got == old || !strings.Contains(got, "┌") {
		t.Fatalf("the text under test is not changed by the renderer; the test proves nothing:\n%s", got)
	}

	// The commands themselves, typed at the prompt.
	m := newTestModel()
	for _, typed := range []string{"/help", "/context", "/git", "/search"} {
		before := len(m.turns)
		m, _ = pressEnter(typeText(m, typed))
		if len(m.turns) == before {
			t.Fatalf("%s put nothing on screen", typed)
		}
		for _, added := range m.turns[before:] {
			if added.role == roleAssistant && !added.plainAnswer() {
				t.Errorf("%s's output would be drawn as markdown: %q", typed, clipForTest(added.text))
			}
		}
	}
	// And a model's answer, arriving over the stream, is not.
	m, _ = pressEnter(typeText(m, "a question"))
	u, _ := m.Update(tokenMsg("**an** answer"))
	m = u.(chatModel)
	if last := m.turns[len(m.turns)-1]; last.role != roleAssistant || last.plainAnswer() {
		t.Errorf("a streamed answer is marked as a command's output: %+v", last)
	}
}

// THE CACHE KEYS ON IT. The same text is drawn two ways, so a cached block for
// one must not be handed back for the other.
func TestTheRenderCacheTellsACommandsOutputFromAnAnswer(t *testing.T) {
	const text = "**a** | b"
	var cache transcriptCache
	for step, turns := range [][]turn{
		{{role: roleAssistant, text: text}},
		{{role: roleAssistant, text: text, local: true}},
		{{role: roleAssistant, text: text}},
		{{role: roleAssistant, text: text, verbatim: true}},
	} {
		if got, want := cache.render(turns, 80), renderTranscript(turns, 80); got != want {
			t.Fatalf("step %d: the cache returned %q, a fresh render is %q", step, got, want)
		}
	}
	if renderTurnBlock(turn{role: roleAssistant, text: text}, 80) == renderTurnBlock(turn{role: roleAssistant, text: text, local: true}, 80) {
		t.Fatal("the two renders are the same; the test proves nothing")
	}
}

// TEXT THAT ALREADY CARRIES COLOUR IS PASSED ON. The sanitiser lets a model's
// own SGR through so highlighted code stays highlighted; parsing markdown
// across those sequences would cut them.
func TestTextWithItsOwnColourIsNotParsed(t *testing.T) {
	const line = "\x1b[38;5;205mfunc\x1b[0m **main**() { _x_ }"
	got := renderAnswer("see:\n"+line, 200)
	if !strings.Contains(got, line) {
		t.Errorf("a line with colour of its own was changed:\n got: %q\nwant it to contain: %q", got, line)
	}
}

// A LINE BUILT TO BE SLOW IS NOT. Each of these made a scanner look back, or
// ahead to the end of the line, once per character -- and the answer is redrawn
// for every token that arrives, so quadratic here is a frozen screen.
//
// THE SIZES ARE CHOSEN SO THE TEST FAILS ON THE SHAPE AND NOT ON THE RUNNER.
// MEASURED 2026-10-06 on this machine, with each bound and with it removed:
//
//	                      size     bounded   unbounded
//	closers, no opener    80,000    55 ms      7.0 s    (resolveEmphasis's floor)
//	underscore closers    80,000    68 ms      8.9 s
//	mixed stars           80,000    96 ms     12.6 s
//	open brackets         80,000    60 ms      1.3 s    (mdMaxLinkText)
//	brackets and a close  80,000    83 ms      1.2 s
//	one long word, wide   80,000    51 ms     40 s     (takeWidth)
//
// The bracket rows are run four times larger than measured, where linear work
// is four times slower and quadratic sixteen. The limit then sits seven times
// or more above every bounded time (the slowest, the brackets, is 350 ms) and
// several times below every unbounded one, so a runner seven times slower
// still passes and one three times faster still fails a scanner that has gone
// quadratic.
func TestALineBuiltToBeSlowIsNot(t *testing.T) {
	const n = 80000
	hostile := []struct{ name, doc string }{
		{"closers, no opener", strings.Repeat("a* ", n)},
		{"underscore closers", strings.Repeat("a_ ", n)},
		{"mixed stars", strings.Repeat("*a** b*** ", n/2)},
		{"open brackets", strings.Repeat("[", 4*n)},
		{"brackets and a close", strings.Repeat("[", 4*n) + "]"},
		{"half links", strings.Repeat("[a](", n/4)},
		{"openers, no closer", strings.Repeat("*a ", n)},
		{"paired underscores", strings.Repeat("_a_ ", n)},
		{"backtick runs", strings.Repeat("` `` ``` ", n/3)},
		{"angle brackets", strings.Repeat("<", n)},
		{"ampersands", strings.Repeat("&", n)},
		{"tildes", strings.Repeat("~~a ", n)},
		{"pipes", strings.Repeat("|", n) + "\n" + strings.Repeat("|-", n)},
		{"quote marks", strings.Repeat(">", n)},
		{"one long word", strings.Repeat("x", 4*n)},
		// A paragraph with no spaces in it, which is every paragraph of
		// Chinese or Japanese: each line once re-measured all that remained
		// (3.4 KB took 1.09 s; FuzzAnAnswerDraws found it).
		{"one long word, wide", strings.Repeat("日本語", n)},
		{"one long word, accented", strings.Repeat("e\u0301", n)},
		{"broken bytes", strings.Repeat("\xc3", n)},
	}
	limit := 2500 * time.Millisecond
	if raceEnabled {
		limit *= 10
	}
	for _, h := range hostile {
		start := time.Now()
		out := renderAnswer(h.doc, 80)
		took := time.Since(start)
		if out == "" {
			t.Errorf("%s: drew nothing", h.name)
		}
		if took > limit {
			t.Errorf("%s: %d bytes took %v to draw (limit %v)", h.name, len(h.doc), took, limit)
		}
		t.Logf("%-22s %7d bytes  %v", h.name, len(h.doc), took.Round(time.Millisecond))
	}
}

// The pens are worked out once per colour profile, not once per answer.
func TestThePensFollowTheColourProfile(t *testing.T) {
	plain := currentPens()
	if plain.open[penStrong] != "" || plain.close[penStrong] != "" {
		t.Fatalf("under the colourless profile the strong pen writes %q ... %q", plain.open[penStrong], plain.close[penStrong])
	}
	if currentPens() != plain {
		t.Error("the pens were built again for the same profile")
	}
	withColorProfile(t, termenv.ANSI256, func() {
		p := currentPens()
		if p == plain || !strings.HasPrefix(p.open[penStrong], "\x1b[") || p.close[penStrong] != "\x1b[0m" {
			t.Errorf("under a colour profile the strong pen writes %q ... %q", p.open[penStrong], p.close[penStrong])
		}
		for pen := mdPen(0); pen < penCount; pen++ {
			if p.open[pen] == "" {
				t.Errorf("pen %d has no style in mdStyles", pen)
			}
		}
	})
	if got := currentPens(); got.open[penStrong] != "" {
		t.Error("the pens did not follow the profile back")
	}
}

func TestFitColumnsSharesTheWidth(t *testing.T) {
	for _, c := range []struct {
		natural, floor []int
		avail          int
		want           []int
	}{
		{[]int{3, 4, 5}, []int{1, 1, 1}, 40, []int{3, 4, 5}},       // room for all
		{[]int{3, 50, 4}, []int{1, 1, 1}, 30, []int{3, 23, 4}},     // the long one wraps, alone
		{[]int{40, 40}, []int{1, 1}, 30, []int{15, 15}},            // two long ones share
		{[]int{40, 40, 40}, []int{1, 1, 1}, 32, []int{11, 11, 10}}, // and the odd columns go to the first
		{[]int{2, 100, 100}, []int{1, 1, 1}, 22, []int{2, 10, 10}},
		{[]int{40, 40}, []int{20, 5}, 30, []int{20, 10}}, // a column is never narrower than its longest word
		{[]int{12, 40, 40}, []int{12, 6, 6}, 30, []int{12, 9, 9}},
	} {
		got := fitColumns(c.natural, c.floor, c.avail)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("fitColumns(%v, %v, %d) = %v, want %v", c.natural, c.floor, c.avail, got, c.want)
		}
		sum := 0
		for i, w := range got {
			sum += w
			if w < c.floor[i] {
				t.Errorf("fitColumns(%v, %v, %d) = %v: column %d is under its floor", c.natural, c.floor, c.avail, got, i)
			}
		}
		if sum > c.avail {
			t.Errorf("fitColumns(%v, %v, %d) = %v, which is %s columns", c.natural, c.floor, c.avail, got, strconv.Itoa(sum))
		}
	}
}

// FuzzAnAnswerDraws throws arbitrary text at the renderer, through the same
// sanitiser a model's tokens come through. Whatever the text and the width, it
// draws; and where the text carries no colour of its own, the three properties
// the rest of the client leans on hold: colour changes no character, no line
// leaves a colour open, and from 24 columns up no line is wider than the
// terminal -- so wrapToWidth, which would cut a colour in half, has nothing to
// do.
func FuzzAnAnswerDraws(f *testing.F) {
	for _, doc := range answerCorpus() {
		f.Add(doc, 40)
	}
	f.Add(answerSample, 24)
	for _, doc := range []string{
		"***a** b* c_d_e __f__ ~~g~~ `h` [i](j.k) <br> &amp; \\*",
		"| a | b |\n|:-:|--:|\n| **c** | `d` |\n",
		"> > > deep\n>\n- [ ] a\n  1) b\n\n---\n# h\n```\ncode\n",
		"<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n",
	} {
		f.Add(doc, 80)
		f.Add(doc, 3)
	}

	f.Fuzz(func(t *testing.T, doc string, width int) {
		if width < 0 || width > 300 || len(doc) > 1<<15 {
			t.Skip()
		}
		doc = sanitizeText(doc)
		plain := renderAnswer(doc, width)
		var coloured string
		withColorProfile(t, termenv.ANSI256, func() { coloured = renderAnswer(doc, width) })
		if strings.IndexByte(doc, '\x1b') >= 0 {
			return // colour of the text's own is passed on, not laid out
		}
		if stripped := ansi.Strip(coloured); stripped != plain {
			t.Fatalf("width %d: colour changed the characters\n  doc: %q\nplain: %q\n  got: %q", width, doc, plain, stripped)
		}
		for n, line := range strings.Split(coloured, "\n") {
			if leavesColourOpen(line) {
				t.Fatalf("width %d, line %d leaves a colour open: %q\n  doc: %q", width, n, line, doc)
			}
			if width >= 24 {
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("width %d, line %d is %d columns: %q\n  doc: %q", width, n, w, line, doc)
				}
			}
		}
		if width >= 24 {
			if again := wrapToWidth(coloured, width); again != coloured {
				t.Fatalf("width %d: wrapToWidth changed a laid-out answer\n  doc: %q\n  was: %q\n  now: %q", width, doc, coloured, again)
			}
		}
	})
}

// The daemon writes the pages a turn read under its answer (daemon/
// websources.go), as ordinary answer text. An address there is something the
// reader copies or clicks, so it is drawn whole and exactly: the angle brackets
// the daemon puts round it make it an autolink, whose contents are not parsed.
// Without them the third address below is the word "Go" in italics with two
// characters missing.
func TestTheSourcesUnderAnAnswerKeepEveryCharacterOfAnAddress(t *testing.T) {
	addresses := []string{
		"https://www.thehindu.com/news/national/tamil-nadu/article123.ece",
		"https://en.wikipedia.org/wiki/Go_(programming_language)",
		"https://example.com/wiki/_Go_/*new*/__init__.py?a=1&amp;b=~2",
	}
	answer := "C. Joseph Vijay is the Chief Minister, according to The Hindu.\n\n" +
		"Sources (pages read for this answer):\n" +
		"- thehindu.com — <" + addresses[0] + ">\n" +
		"- en.wikipedia.org — <" + addresses[1] + ">\n" +
		"- example.com — <" + addresses[2] + ">"

	got := renderAnswer(answer, 200)
	for _, address := range addresses {
		if !strings.Contains(got, address) {
			t.Errorf("the address %q is not on screen as written:\n%s", address, got)
		}
	}
	if strings.ContainsAny(got, "<>") {
		t.Errorf("the brackets that delimit an address were drawn:\n%s", got)
	}
	for _, want := range []string{"Sources (pages read for this answer):", "thehindu.com — ", "en.wikipedia.org — "} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing:\n%s", want, got)
		}
	}

	// At a width an address does not fit, it is broken across lines and loses
	// nothing: joined back up, every character is there.
	narrow := renderAnswer(answer, 40)
	joined := strings.Join(strings.Fields(narrow), "")
	for _, address := range addresses {
		if !strings.Contains(joined, address) {
			t.Errorf("at 40 columns the address %q lost a character:\n%s", address, narrow)
		}
	}
}
