package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// PROVING THE KEY, RATHER THAN ASSUMING IT.
//
// A `connect` that stores a key and prints "connected" has checked nothing, and
// the first time the user learns the key is wrong is a failed prompt later,
// surfacing as a provider outage. That is the same false-claim shape the sandbox
// probes exist to refuse (presence is not capability), so this asks.
//
// It asks ONE question and reports what came back -- including, crucially, that
// it could not tell. An OpenAI-compatible base is not required to authenticate
// GET /models, and many serve it wide open; treating a 200 from it as proof would
// be a check that passes for every string. So there is a fourth outcome, and it
// is not a failure: the key is saved and the report says it was not proven.

// verifyOutcome is what the provider actually told us.
type verifyOutcome int

const (
	// verifyAccepted: an endpoint that authenticates accepted this key.
	verifyAccepted verifyOutcome = iota
	// verifyRejected: an endpoint answered that this key is not valid.
	verifyRejected
	// verifyInconclusive: the base answered, but nothing it answered proves the
	// key. Not an error, and not a pass.
	verifyInconclusive
	// verifyUnreachable: no answer at all.
	verifyUnreachable
)

// verification is an outcome plus the sentence to show the user for it.
type verification struct {
	Outcome verifyOutcome
	Detail  string
}

// proven reports whether the provider authenticated the key.
func (v verification) proven() bool { return v.Outcome == verifyAccepted }

// maxVerifyBodyBytes caps what a verification response may put in memory. Capped
// like every other network read here (config.go's model status, provider.go's
// error bodies, webfetch's maxBytes): the context bounds how LONG a hostile base
// can hold us, not how MUCH it can send.
const maxVerifyBodyBytes = 1 << 20

// verifyKey makes at most two requests and classifies the result.
//
// First GET {base}/key, the endpoint whose whole purpose is to describe the
// calling key -- it authenticates by construction, so a 200 from it is real proof
// and a 401 is a real refusal. A base that does not have it answers 404/405, and
// then GET {base}/models is asked as a fallback: a 401 there still PROVES the key
// is bad, while a 200 proves nothing, because that endpoint is commonly public.
func verifyKey(ctx context.Context, client *http.Client, apiBase, apiKey string) verification {
	base := strings.TrimRight(apiBase, "/")

	status, body, err := getWithKey(ctx, client, base+"/key", apiKey)
	switch {
	case err != nil:
		// Fall through to /models: this may be a base that has no /key at all and
		// a transport-level oddity rather than an unreachable host. The fallback
		// decides which.
	case status == http.StatusOK:
		return verification{verifyAccepted, describeKeyResponse(body)}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return verification{verifyRejected, fmt.Sprintf("the provider refused it (HTTP %d)", status)}
	}

	modelsStatus, _, modelsErr := getWithKey(ctx, client, base+"/models", apiKey)
	switch {
	case modelsErr != nil:
		return verification{verifyUnreachable, fmt.Sprintf("%s did not answer: %v", base, modelsErr)}
	case modelsStatus == http.StatusUnauthorized || modelsStatus == http.StatusForbidden:
		return verification{verifyRejected, fmt.Sprintf("the provider refused it (HTTP %d)", modelsStatus)}
	case modelsStatus == http.StatusOK:
		return verification{verifyInconclusive, fmt.Sprintf("%s is reachable, but it served /models without "+
			"asking for the key and has no /key endpoint, so nothing here proves the key is valid", base)}
	default:
		return verification{verifyInconclusive, fmt.Sprintf("%s answered HTTP %d, which neither accepts nor "+
			"refuses the key", base, modelsStatus)}
	}
}

// getWithKey performs one authenticated GET and returns the status and a capped
// body. The key travels in the Authorization header and nowhere else -- never in
// the URL, which would put it in the provider's access log.
func getWithKey(ctx context.Context, client *http.Client, url, apiKey string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVerifyBodyBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// describeKeyResponse turns a /key response into one human sentence, when it
// carries anything worth saying. Best-effort by design: the key is already proven
// by the 200, so a body this cannot parse must not downgrade that -- it only
// costs the user a nicety.
func describeKeyResponse(body []byte) string {
	var parsed struct {
		Data struct {
			Label      string   `json:"label"`
			Usage      *float64 `json:"usage"`
			Limit      *float64 `json:"limit"`
			IsFreeTier *bool    `json:"is_free_tier"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "the provider accepted it"
	}
	var parts []string
	if parsed.Data.Label != "" {
		parts = append(parts, fmt.Sprintf("key %q", parsed.Data.Label))
	}
	if parsed.Data.Limit != nil {
		parts = append(parts, fmt.Sprintf("credit limit %.2f", *parsed.Data.Limit))
	} else if parsed.Data.IsFreeTier != nil && *parsed.Data.IsFreeTier {
		parts = append(parts, "free tier")
	}
	if parsed.Data.Usage != nil {
		parts = append(parts, fmt.Sprintf("used %.2f", *parsed.Data.Usage))
	}
	if len(parts) == 0 {
		return "the provider accepted it"
	}
	return "the provider accepted it: " + strings.Join(parts, ", ")
}
