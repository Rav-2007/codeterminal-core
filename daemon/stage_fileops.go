package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// THE WORKING COPY FOLLOWS A DELETE OR A MOVE (fileops.go). The real file has
// already changed when these run; the copy is brought into line so the rest of
// the turn reads what is on disk, and what the copy offers at the end of the
// turn is still the agent's edits -- at the new path, after a move -- and never
// a "deleted in the copy" note for something the user agreed to delete.
// Every method is a no-op without a copy.
//
// Each of the three mirror methods counts in st.reshaped: a checkpoint taken
// before one is never restored (checkpoint.go), as it would put back what the
// user agreed to remove.

// stageKey is rel as the copy keys its files: the native, cleaned form
// filepath.Rel gives. The tools' paths are the model's, with forward slashes,
// and on Windows "pkg/a.go" is not the key "pkg\a.go" -- a moved file then read
// as deleted in the copy.
func stageKey(rel string) string {
	return filepath.Clean(filepath.FromSlash(rel))
}

// under reports whether key is rel or inside it.
func under(key, rel string) bool {
	return key == rel || strings.HasPrefix(key, rel+string(filepath.Separator))
}

// renamed is key with its rel prefix replaced by to.
func renamed(key, rel, to string) string {
	return to + strings.TrimPrefix(key, rel)
}

func (st *stagedWorkspace) forgetUnder(rel string) {
	for k := range st.manifest {
		if under(k, rel) {
			delete(st.manifest, k)
		}
	}
	for k := range st.touched {
		if under(k, rel) {
			delete(st.touched, k)
		}
	}
	for k := range st.byCommand {
		if under(k, rel) {
			delete(st.byCommand, k)
		}
	}
	kept := st.order[:0]
	for _, k := range st.order {
		if !under(k, rel) {
			kept = append(kept, k)
		}
	}
	st.order = kept
}

func (st *stagedWorkspace) renameUnder(rel, to string) {
	manifest := map[string]stagedFile{}
	for k, v := range st.manifest {
		if under(k, rel) {
			k = renamed(k, rel, to)
		}
		manifest[k] = v
	}
	st.manifest = manifest
	touched := map[string]bool{}
	for k, v := range st.touched {
		if under(k, rel) {
			k = renamed(k, rel, to)
		}
		touched[k] = v
	}
	st.touched = touched
	for k, v := range st.byCommand {
		if under(k, rel) {
			delete(st.byCommand, k)
			st.byCommand[renamed(k, rel, to)] = v
		}
	}
	for i, k := range st.order {
		if under(k, rel) {
			st.order[i] = renamed(k, rel, to)
		}
	}
}

func (p *proposalSink) mirrorDelete(rel string) {
	if p == nil || p.stage == nil {
		return
	}
	rel = stageKey(rel)
	_ = os.RemoveAll(filepath.Join(p.stage.root, rel))
	p.stage.forgetUnder(rel)
	p.stage.reshaped++
}

func (p *proposalSink) mirrorMove(from, to string) {
	if p == nil || p.stage == nil {
		return
	}
	from, to = stageKey(from), stageKey(to)
	src, dst := filepath.Join(p.stage.root, from), filepath.Join(p.stage.root, to)
	if _, err := os.Lstat(src); err == nil {
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		_ = os.Rename(src, dst)
	}
	p.stage.renameUnder(from, to)
	p.stage.reshaped++
}

// mirrorRestore copies a restored file (or folder) into the copy, recorded as
// what the project holds -- not as an edit of the agent's.
func (p *proposalSink) mirrorRestore(rel string) {
	if p == nil || p.stage == nil {
		return
	}
	st := p.stage
	st.reshaped++
	_ = filepath.WalkDir(filepath.Join(st.real, rel), func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil //nolint:nilerr
		}
		r, _ := filepath.Rel(st.real, path)
		dst := filepath.Join(st.root, r)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		// Streamed, never held whole: a restored file may be any size.
		if copyFile(path, dst) != nil {
			return nil
		}
		ri, e1 := os.Stat(path)
		si, e2 := os.Stat(dst)
		if e1 == nil && e2 == nil {
			st.manifest[r] = stagedFile{realSize: ri.Size(), realMod: ri.ModTime(), stageSize: si.Size(), stageMod: si.ModTime()}
		}
		return nil
	})
}

// stagedOnlyDelete deletes what exists only in the copy -- a file the agent
// created this turn -- and reports whether there was one.
func (p *proposalSink) stagedOnlyDelete(rel string) bool {
	if p == nil || p.stage == nil {
		return false
	}
	rel = stageKey(rel)
	path := filepath.Join(p.stage.root, rel)
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	if _, known := p.stage.manifest[rel]; known {
		return false
	}
	_ = os.RemoveAll(path)
	p.stage.forgetUnder(rel)
	return true
}

// stagedOnlyMove moves what exists only in the copy, the same way.
func (p *proposalSink) stagedOnlyMove(from, to string) bool {
	if p == nil || p.stage == nil {
		return false
	}
	from, to = stageKey(from), stageKey(to)
	src := filepath.Join(p.stage.root, from)
	if _, err := os.Lstat(src); err != nil {
		return false
	}
	if _, known := p.stage.manifest[from]; known {
		return false
	}
	dst := filepath.Join(p.stage.root, to)
	if _, err := os.Lstat(dst); err == nil {
		return false
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	if os.Rename(src, dst) != nil {
		return false
	}
	p.stage.renameUnder(from, to)
	return true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
