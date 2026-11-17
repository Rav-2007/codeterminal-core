package main

import (
	"errors"
	"fmt"

	"mochiii/daemon/mcp"
)

// WHAT THE MODEL IS TOLD WHEN A SERVER'S TOOL CANNOT RUN.
//
// The loop used to have two sentences for everything that went wrong between
// the registry and a third-party server -- "the tool's server is unavailable"
// and "the tool failed to run" -- and one for everything that failed to resolve:
// 'there is no tool called "x" available in this turn'. So a server that died
// was reported, from the second call on, as a tool that does not exist, and a
// server's own refusal was reported as no reason at all.
//
// The words now come from the error itself (mcp.ModelFacing): the mcp package
// knows whether a server stopped, came back, stayed down or sent too much, and
// the numbers behind it. What is left here is the case of an error that says
// nothing more than "this server is not answering".

// callFailureText is what the model, and the user's row for the call, are told
// when registry.Call returned err for tool.
func callFailureText(tool mcp.Tool, err error) string {
	var facing mcp.ModelFacing
	if errors.As(err, &facing) {
		return facing.ModelMessage()
	}
	if errors.Is(err, mcp.ErrServerUnavailable) {
		return fmt.Sprintf("the server %q is unavailable, so the tool %q did not run. The tool is not missing; "+
			"its server did not answer.", tool.Server, tool.Name)
	}
	return "the tool failed to run"
}

// lookupFailureText is what the model is told when a tool it named could not be
// resolved because the lookup itself failed on a server that is not answering --
// and ok says it was that, and not the model naming something that is not there.
func lookupFailureText(qualified string, err error) (text string, ok bool) {
	var facing mcp.ModelFacing
	if errors.As(err, &facing) {
		return facing.ModelMessage(), true
	}
	if errors.Is(err, mcp.ErrServerUnavailable) {
		server, tool, splitErr := mcp.SplitQualifiedName(qualified)
		if splitErr != nil {
			return "", false
		}
		return fmt.Sprintf("the server %q is unavailable, so the tool %q cannot run now. The tool is not missing; "+
			"its server did not answer.", server, tool), true
	}
	return "", false
}
