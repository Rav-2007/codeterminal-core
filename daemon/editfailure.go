package main

import (
	"errors"
	"fmt"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// A REFUSED EDIT SAYS WHAT KIND OF REFUSAL IT IS, AND WHAT TO DO NEXT.
//
// FOUND 2026-10-15, live: "write a simple login page using html in the
// new_file". ~/Desktop/new_file was an empty FOLDER, propose_edit answered "is
// a directory" -- and the model, told what was wrong but not what to do, sent
// back nothing at all, three times. The user saw "Used 1 tool · 1 failed" and
// an answer cut off for an empty reply, with no word of the folder.
//
// So a refused edit carries, on a line of its own after the reason (the chat
// row shows the first line only, toolFailureReason), the daemon's note: the
// category, that nothing was proposed or written, whether the same arguments
// could ever succeed, and the next step for that category.

// Edit-failure categories, as the model reads them. Stable words, so a log or
// a test can match on them.
const (
	editFailIsDirectory = "target_is_directory"
	editFailPathRefused = "path_refused"
	editFailRejected    = "edit_rejected"
)

// editFailureCategory names the kind of refusal err is.
func editFailureCategory(err error) string {
	switch {
	case errors.Is(err, editapply.ErrIsDirectory):
		return editFailIsDirectory
	case errors.Is(err, editapply.ErrPathRefused):
		return editFailPathRefused
	}
	return editFailRejected
}

// editFailure is propose_edit's result for a refused edit: reason is the text
// already fit for the model (no absolute path), path is the path it sent.
func editFailure(path, reason string, err error) (mcp.Result, error) {
	category := editFailureCategory(err)
	var next string
	switch category {
	case editFailIsDirectory:
		next = fmt.Sprintf("%s is a folder that already exists. Write a file INSIDE it instead -- pick a name "+
			"that fits the request (a web page: %s/index.html) and tell the user the file you chose -- or ask "+
			"the user which file they meant.", path, trimSlash(path))
	case editFailPathRefused:
		next = "Mochiii never writes this path, with any arguments. Tell the user why, and offer a place it " +
			"can write: the project, or an ordinary folder in their home folder such as ~/Desktop."
	default:
		next = "Fix the arguments -- read the file first if the search text did not match -- and propose it once more."
	}
	return toolError("that edit cannot be applied: %s\n[Mochiii: propose_edit FAILED (%s). Nothing was proposed "+
		"or written, so do not tell the user it was. The same arguments will fail the same way. %s]",
		reason, category, next)
}

func trimSlash(p string) string {
	for len(p) > 1 && (p[len(p)-1] == '/' || p[len(p)-1] == '\\') {
		p = p[:len(p)-1]
	}
	return p
}
