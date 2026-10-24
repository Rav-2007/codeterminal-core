package main

import (
	"encoding/json"
	"os"
	"strings"
)

// HOW A REQUEST TO A MODEL SERVER ON THIS MACHINE IS WORDED (1.4.0).
//
// Every address no provider list names used to get OpenRouter's routing
// dialect, a server on this machine included, and the routing dialect is never
// adapted after a refusal (requestDialect.adapt): nothing may be taken out of a
// request to OpenRouter or the proxy because a response said so. So a local
// server that refused the output cap refused every question. vLLM does exactly
// that when the input plus max_tokens exceeds the model's length, and the cap
// was always defaultMaxOutputTokens, 32,768.
//
// A server identified as Ollama, LM Studio, llama.cpp or vLLM now gets the
// plain request those servers document, which IS adapted after a refusal, with
// an output cap that leaves the prompt room in the window read from the server.
// "Identified" means a fit or a tool check found its kind (localKnown), and
// only the terminal app asks for those. A loopback address nothing identified
// -- a gateway that may forward to OpenRouter -- keeps the routing dialect byte
// for byte, and so does every request in proxy mode.

// servedLocally reports whether apiBase is a model server on this machine that
// a fit or a tool check identified.
func servedLocally(apiBase string) bool {
	if os.Getenv("MOCHIII_USE_PROXY") == "true" {
		return false
	}
	_, ok := localKnown.kinds.Load(strings.TrimRight(apiBase, "/"))
	return ok
}

const (
	// localOutputMargin is left between the prompt and the end of the window.
	localOutputMargin = 64
	// localOutputFloor is the least output cap worth sending. Below it an answer
	// is cut off before it says anything, and the server's own limit does better.
	localOutputFloor = 256
)

// fitOutputCap is want, lowered to leave the prompt room in a window of the
// given size. The prompt is estimated at localCharsPerToken characters a token,
// which errs towards a smaller cap: one a little small cuts an answer short and
// says so, while one too large is refused before a word is written.
func fitOutputCap(want, window int, messages []chatMessage, tools []toolSpec) int {
	if window <= 0 || want <= 0 {
		return want
	}
	size := 0
	if b, err := json.Marshal(messages); err == nil {
		size += len(b)
	}
	if b, err := json.Marshal(tools); err == nil {
		size += len(b)
	}
	room := window - size/localCharsPerToken - localOutputMargin
	if room < localOutputFloor {
		room = localOutputFloor
	}
	return min(want, room)
}
