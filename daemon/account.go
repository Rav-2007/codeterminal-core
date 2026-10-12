package main

import (
	"os"
	"strings"

	"mochiii/protocol"
)

// shownInForce adds what this daemon is SENDING NOW to the answer to
// ConnectRequest.Show: the address, where its key came from, the model prompts
// go to and how many it could choose between. See protocol.ConnectInForce for
// why "what is stored" was not enough, and for who reads this.
//
// IT ASKS NOBODY. Everything here is state the daemon already holds or one
// small file it already reads on this path -- no request to the provider -- so
// a client may ask at every start and after every change without that costing
// the user a network round trip, or a provider a key check, each time.
//
// A separate file, and wrapped round the existing return rather than written
// into it, for the reason daemon/serverextras.go gives.
func (s *Server) shownInForce(stored storedCredential, resp protocol.ConnectResponse) protocol.ConnectResponse {
	key, base := s.credentials()
	source := keySourceOf(key)

	resp.ActiveAPIBase = firstNonEmpty(base, defaultAPIBase)
	resp.KeySource = source
	// The proxy's credential is not a provider key, and showing four characters
	// of it under "API key" would say it was.
	if source != protocol.KeySourceNone && source != protocol.KeySourceProxy {
		resp.ActiveMaskedKey = maskKey(key)
	}

	decision := s.route("", "")
	resp.Model = decision.Tier
	if decision.Slug != decision.Tier {
		resp.ModelSlug = decision.Slug
	}
	// --model names the model outright and there is no tier behind it: the id
	// is the only name it has.
	if s.modelOverride != "" {
		resp.Model, resp.ModelSlug = s.modelOverride, ""
	}
	if cfg := s.tierConfig(); cfg != nil {
		for _, tier := range cfg.Tiers {
			if tier.Active && strings.TrimSpace(tier.Slug) != "" {
				resp.ModelCount++
			}
		}
	}

	// "REMEMBERED" IS CLAIMED ONLY FOR A CHOICE A RESTART WOULD FIND: written
	// beside a stored key that is the one in force, for the address in force,
	// and still the model in force. chooseModel writes it under exactly these
	// conditions; this reads them back rather than trusting that it did.
	resp.ModelSaved = source == protocol.KeySourceStored &&
		stored.configured() && stored.issuedFor(resp.ActiveAPIBase) &&
		stored.ChosenModel != "" && stored.ChosenModel == decision.Tier
	return resp
}

// keySourceOf says where the key in force came from. The order is the daemon's
// own order of precedence at start (main.go): proxy mode replaces the key
// altogether, then the environment, then the stored credential.
func keySourceOf(key string) string {
	switch {
	case os.Getenv("MOCHIII_USE_PROXY") == "true":
		return protocol.KeySourceProxy
	case strings.TrimSpace(os.Getenv("MOCHIII_API_KEY")) != "":
		return protocol.KeySourceEnvironment
	case launchKeySupplied.Load() && strings.TrimSpace(key) != "":
		// The launcher's key takes the stored slot and the file is then not
		// consulted (launchcred.go), so "stored" would name a file that does
		// not hold the key in force.
		return protocol.KeySourceLauncher
	case strings.TrimSpace(key) != "":
		return protocol.KeySourceStored
	default:
		return protocol.KeySourceNone
	}
}
