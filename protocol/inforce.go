package protocol

// ConnectInForce is what the daemon is SENDING NOW, added to the answer to
// ConnectRequest.Show.
//
// WHY IT IS NOT THE SAME AS WHAT IS STORED. Show used to describe the stored
// credential and nothing else, and for most users the two are one thing. They
// come apart exactly when a user most needs to be told: a key in the daemon's
// environment outranks the stored one, proxy mode uses neither, and a daemon
// started with a local model server has no key at all. A client drawing "you
// are connected to X" from the stored credential would name the wrong provider
// in each of those, with nothing on screen to contradict it.
//
// WHO READS IT (OWNER, 2026-10-12): the terminal client's /account, the line
// under its prompt that names the model and provider, and the "(current)" mark
// in its provider list.
//
// EMBEDDED IN ConnectResponse, so these are ordinary top-level keys on the
// wire. It is a separate type in a separate file only because protocol.go must
// not grow for it -- the same reason, measured, that daemon/serverextras.go
// gives for daemon/server.go.
type ConnectInForce struct {
	// ActiveAPIBase is the address prompts are sent to. APIBase, beside it in
	// the response, stays what it always was with Show: the stored key's.
	ActiveAPIBase string `json:"active_api_base,omitempty"`

	// KeySource says where the key in force came from: one of the KeySource
	// constants. NEVER EMPTY from a daemon that knows this field, which is how
	// a client tells such a daemon from an older one without a feature flag.
	KeySource string `json:"key_source,omitempty"`

	// ActiveMaskedKey is the key in force, masked by the daemon like MaskedKey.
	// Empty with KeySourceNone, and in proxy mode -- the proxy's credential is
	// not a provider key and is not described as one.
	ActiveMaskedKey string `json:"active_masked_key,omitempty"`

	// ModelSlug is what the provider calls the model in force, when Model -- the
	// name /model lists it under -- is a tier name rather than the id itself.
	ModelSlug string `json:"model_slug,omitempty"`
}

// The values of ConnectInForce.KeySource.
const (
	// KeySourceStored: the key written by Connect, from the credentials file.
	KeySourceStored = "stored"
	// KeySourceEnvironment: MOCHIII_API_KEY in the daemon's own environment.
	KeySourceEnvironment = "environment"
	// KeySourceLauncher: handed to the daemon on stdin by the program that
	// started it -- the editor, from its own secret store
	// (--credentials-from-stdin). It is in no file this daemon reads, so it is
	// not "stored", and a model chosen beside it is not remembered. Added when
	// this was merged with the launch credential (2026-10-12): until then such
	// a key was reported as stored, which named a file that did not hold it.
	KeySourceLauncher = "launcher"
	// KeySourceProxy: proxy mode. The daemon holds a proxy credential and no
	// provider key.
	KeySourceProxy = "proxy"
	// KeySourceNone: no key is in force. Expected for a model server on this
	// machine; otherwise the next prompt will be refused (NeedsAPIKey).
	KeySourceNone = "none"
)
