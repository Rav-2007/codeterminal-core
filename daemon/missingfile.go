package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mochiii/editapply"
)

// A FILE THAT IS NOT THERE IS ANSWERED ONCE, WITH AUTHORITY.
//
// MEASURED 2026-10-06 and reproduced 2026-10-08: asked about a file that does
// not exist, read_file said
//
//	cannot read config/settings.yaml: resolving config/settings.yaml: no such file
//
// and nothing else. That is true of one path and silent about the project, so
// a model does the reasonable thing and tries the next path -- 9 to 17 model
// calls and 36K to 76K tokens on a real model to conclude "that file does not
// exist", and a scripted one that never gives up ran to the iteration ceiling:
// 17 calls. Meanwhile conf/settings.yml sat one folder away and was never
// mentioned.
//
// The daemon can answer the question the model is really asking -- "is it
// anywhere?" -- in one walk of the project, so it does, in the result of the
// first failed read:
//
//   - the same name somewhere else: that path, and "read that";
//   - no such name, but close ones: those, and "if none of these, it does not
//     exist";
//   - nothing: "no file of that name exists anywhere in this project (N files
//     checked)", and to say so to the user rather than guess more paths.
//
// The count is what makes it a fact instead of an opinion: this is what was
// looked at.
//
// WHAT IT NEVER DOES is name a file the model could not have listed. The walk
// prunes what list_directory and grep prune -- protected folders, dependency
// folders, anything git ignores -- and every candidate goes through the gate
// read_file itself uses, so a secret-shaped name is never a suggestion. And a
// request that ITSELF names a protected or secret path gets the resolver's
// answer and nothing more: saying "there is no .env here, but there is one in
// deploy/" would be this function listing what the listing hides.

// maxMissingScanFiles bounds the walk. Past it the answer says how many files
// were looked at and stops short of "nowhere".
const maxMissingScanFiles = 20000

// missingScanLimit is maxMissingScanFiles, as a variable so that a test can
// reach the "stopped early" answer without making twenty thousand files.
var missingScanLimit = maxMissingScanFiles

// maxMissingSuggestions bounds the names offered back.
const maxMissingSuggestions = 5

// missingPathAnswer is what a read or a listing of asked says when asked does
// not exist inside the project. ok is false when this is not that case -- the
// path is outside the project, is refused by name, or does exist -- and the
// caller's own message stands.
func (s *Server) missingPathAnswer(ctx context.Context, verb, asked string, wantDir bool) (answer string, ok bool) {
	root := ""
	if st := stageFromCtx(ctx); st != nil {
		root = st.root
	} else {
		var err error
		if root, err = s.realWorkspaceRoot(); err != nil {
			return "", false
		}
	}
	rel, ok := projectRelative(root, asked)
	if !ok {
		return "", false
	}
	// A path the gates refuse by name is the resolver's to answer, not ours.
	parts := editapply.SplitComponents(rel)
	if len(parts) == 0 {
		return "", false
	}
	for _, part := range parts {
		if editapply.IsProtectedDirName(part) || editapply.MatchesSecretName(part) {
			return "", false
		}
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil || !os.IsNotExist(err) {
		return "", false
	}

	names, complete := projectNames(ctx, root, wantDir)
	if ctx.Err() != nil {
		return "", false
	}
	base := parts[len(parts)-1]
	same, close := similarNames(names, rel, base)

	kind, kinds := "file", "files"
	if wantDir {
		kind, kinds = "folder", "folders"
	}
	checked := fmt.Sprintf("%d %s checked", len(names), kinds)
	var b strings.Builder
	fmt.Fprintf(&b, "cannot %s %s: no such %s.", verb, asked, kind)
	switch {
	case len(same) > 0:
		fmt.Fprintf(&b, " A %s named %s exists at: %s. Use that path.", kind, base, strings.Join(same, ", "))
	case !complete:
		// Not "nowhere": the walk stopped early, and saying more than was
		// looked at would be the confident wrong answer this exists to end.
		fmt.Fprintf(&b, " No %s named %s was found in the first %s of this project.", kind, base, checked)
		if len(close) > 0 {
			fmt.Fprintf(&b, " The closest names: %s.", strings.Join(close, ", "))
		}
	case len(close) > 0:
		fmt.Fprintf(&b, " No %s named %s exists anywhere in this project (%s). The closest names: %s. "+
			"If none of these is the one you want, it does not exist: tell the user so, and do not try more paths.",
			kind, base, checked, strings.Join(close, ", "))
	default:
		fmt.Fprintf(&b, " No %s named %s exists anywhere in this project (%s), and nothing has a similar name. "+
			"It does not exist: tell the user so, and do not try more paths.", kind, base, checked)
	}
	return b.String(), true
}

// projectRelative returns asked as a clean, slash-separated path under root,
// or false for a path that points outside it.
func projectRelative(root, asked string) (string, bool) {
	asked = strings.TrimSpace(asked)
	if asked == "" {
		return "", false
	}
	if abs, isAbs := expandOutsidePath(asked); isAbs {
		// Made relative to the project. A path outside it comes out starting
		// with "..", or not at all (another volume), and is refused below by
		// the one check that covers a relative path too.
		r, err := filepath.Rel(root, resolveOutsidePath(abs))
		if err != nil {
			return "", false
		}
		asked = r
	}
	rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(asked, "\\", "/"))))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// projectNames lists the project's files (or folders), slash-separated and
// relative, under the rules list_directory and grep follow. complete is false
// when the walk met maxMissingScanFiles first.
func projectNames(ctx context.Context, root string, dirs bool) (names []string, complete bool) {
	ignore := newGitignoreMatcher(root)
	complete = true
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil || path == root {
			return nil //nolint:nilerr // an unreadable entry is left out, as the index leaves it out
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // not under root: not this project's
		}
		slash := filepath.ToSlash(rel)
		if editapply.IsLinkLike(d.Type()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if isPrunedDir(d.Name()) || stageLinkedDirs[d.Name()] || ignore.matchDir(slash) {
				return fs.SkipDir
			}
			if dirs {
				names = append(names, slash)
			}
		} else {
			if dirs || !d.Type().IsRegular() || ignore.matchFile(slash) {
				return nil
			}
			// The gate read_file uses: nothing is suggested that could not be read.
			if _, err := editapply.ResolveSafeTargetPath(root, rel); err != nil {
				return nil //nolint:nilerr // a refused file is simply not named
			}
			names = append(names, slash)
		}
		if len(names) >= missingScanLimit {
			complete = false
			return fs.SkipAll
		}
		return nil
	})
	return names, complete
}

// similarNames picks, from the project's names, the ones worth offering for a
// path that does not exist: same has the asked base name exactly (whatever its
// case), close has the same name with another ending, or a name that holds the
// asked one's stem.
func similarNames(names []string, askedRel, base string) (same, close []string) {
	lowerBase := strings.ToLower(base)
	stem := strings.TrimSuffix(lowerBase, strings.ToLower(filepath.Ext(base)))
	if stem == "" {
		stem = lowerBase // ".gitkeep": the name is all there is
	}
	type scored struct {
		name  string
		score int
	}
	var near []scored
	for _, name := range names {
		if name == askedRel {
			continue
		}
		nb := strings.ToLower(name[strings.LastIndexByte(name, '/')+1:])
		nstem := strings.TrimSuffix(nb, filepath.Ext(nb))
		switch {
		case nb == lowerBase:
			same = append(same, name)
		case nstem == stem:
			near = append(near, scored{name, 0})
		// A stem of one or two letters is in every name; it says nothing.
		case len(stem) >= 4 && strings.Contains(nb, stem):
			near = append(near, scored{name, 1})
		case len(nstem) >= 4 && strings.Contains(stem, nstem):
			near = append(near, scored{name, 2})
		}
	}
	sort.Strings(same)
	if len(same) > maxMissingSuggestions {
		same = same[:maxMissingSuggestions]
	}
	sort.SliceStable(near, func(i, j int) bool {
		if near[i].score != near[j].score {
			return near[i].score < near[j].score
		}
		return near[i].name < near[j].name
	})
	for i, n := range near {
		if i == maxMissingSuggestions {
			break
		}
		close = append(close, n.name)
	}
	return same, close
}
