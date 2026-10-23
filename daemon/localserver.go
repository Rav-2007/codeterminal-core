package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mochiii/protocol"
)

// MODEL SERVERS ON THIS MACHINE (1.3.0): finding them, telling them apart, and
// asking how much a model may read. See protocol/local.go for why.
//
// ONLY 127.0.0.1. Discovery never names any other host: what is probed is a
// fixed list of loopback addresses, so nothing here can be pointed at the
// network, and a request carries no key -- these servers want none.

// localServerKind is one kind of server and the port it listens on by default.
type localServerKind struct {
	id, name string
	port     int
}

// localServerKinds are the servers Discover looks for, in the order they are
// listed. A var so tests can point it at stand-ins on free ports.
var localServerKinds = []localServerKind{
	{"ollama", "Ollama", 11434},
	{"lmstudio", "LM Studio", 1234},
	{"llamacpp", "llama.cpp", 8080},
	{"vllm", "vLLM", 8000},
}

// localHTTP talks to servers on this machine: short timeouts, and no proxy,
// since a loopback address is never meant to go through one.
var localHTTP = &http.Client{
	Timeout:   3 * time.Second,
	Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
}

// localPreloadTimeout bounds asking Ollama to load a model, which reads it from
// disk into memory: seconds for a small one, longer for a big one on a slow disk.
const localPreloadTimeout = 2 * time.Minute

// discoverLocalServers lists the servers that answer the OpenAI model list on
// this machine's usual ports, each told apart by an endpoint only that kind
// serves. A port that answers something else -- any web app on 8080 -- is not
// listed, and neither is one that answers with no models unless it is a kind
// that can be told apart -- an Ollama with nothing downloaded yet is still
// worth naming, so the person can be told to download a model.
func discoverLocalServers(ctx context.Context) []protocol.LocalServer {
	found := make([]*protocol.LocalServer, len(localServerKinds))
	var wg sync.WaitGroup
	for i, k := range localServerKinds {
		wg.Add(1)
		go func(i int, k localServerKind) {
			defer wg.Done()
			base := fmt.Sprintf("http://127.0.0.1:%d/v1", k.port)
			models, status, _, err := listModels(ctx, localHTTP, base, "")
			if err != nil || status != http.StatusOK {
				return
			}
			srv := protocol.LocalServer{APIBase: base, Models: chatModelIDs(models)}
			switch kind := localKindAt(ctx, base); {
			case kind.id != "":
				srv.Kind, srv.Name = kind.id, kind.name
			case len(srv.Models) > 0:
				srv.Name = "a model server"
			default:
				// Not a kind it could tell, and no model listed: a web app
				// that answers every path, not a model server.
				return
			}
			found[i] = &srv
		}(i, k)
	}
	wg.Wait()
	var out []protocol.LocalServer
	for _, s := range found {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// localKindAt tells which server answers at base by the endpoint only that kind
// serves. The port is not trusted: LM Studio can be moved to 8080, and anything
// can listen on 8000.
func localKindAt(ctx context.Context, base string) localServerKind {
	origin := localOrigin(base)
	if origin == "" {
		return localServerKind{}
	}
	var ollama struct {
		Version string `json:"version"`
	}
	if getLocalJSON(ctx, origin+"/api/version", &ollama) && ollama.Version != "" {
		return localKindByID("ollama")
	}
	var lmstudio struct {
		Models []json.RawMessage `json:"models"`
		Data   []json.RawMessage `json:"data"`
	}
	if getLocalJSON(ctx, origin+"/api/v1/models", &lmstudio) && lmstudio.Models != nil {
		return localKindByID("lmstudio")
	}
	if getLocalJSON(ctx, origin+"/api/v0/models", &lmstudio) && lmstudio.Data != nil {
		return localKindByID("lmstudio")
	}
	var props struct {
		Settings *struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if getLocalJSON(ctx, origin+"/props", &props) && props.Settings != nil {
		return localKindByID("llamacpp")
	}
	var vllm struct {
		Data []struct {
			MaxModelLen int `json:"max_model_len"`
		} `json:"data"`
	}
	if getLocalJSON(ctx, strings.TrimRight(base, "/")+"/models", &vllm) && len(vllm.Data) > 0 && vllm.Data[0].MaxModelLen > 0 {
		return localKindByID("vllm")
	}
	return localServerKind{}
}

func localKindByID(id string) localServerKind {
	for _, k := range localServerKinds {
		if k.id == id {
			return k
		}
	}
	return localServerKind{}
}

// localOrigin is base without its path -- http://127.0.0.1:11434 for
// http://127.0.0.1:11434/v1 -- or "" for anything that is not a loopback
// http(s) address.
func localOrigin(base string) string {
	if !isLoopbackBase(base) {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// getLocalJSON GETs url and decodes a 200 answer into out.
func getLocalJSON(ctx context.Context, url string, out any) bool {
	return doLocalJSON(ctx, localHTTP, http.MethodGet, url, nil, out)
}

func doLocalJSON(ctx context.Context, client *http.Client, method, url string, body any, out any) bool {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return false
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return false
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) == nil
}

// localWindow is what a server says about one model's context.
type localWindow struct {
	kind    localServerKind
	window  int // the context the server gives the model now; 0 if unknown
	trained int // the most the model can read; 0 if unknown
}

// localContextWindow asks the server at base how much model may read.
//
// Ollama gives a model the context it was LOADED with -- 4096 tokens unless the
// server was started with OLLAMA_CONTEXT_LENGTH -- and reports it only while
// the model is loaded, so a model that is not loaded yet is loaded first: the
// documented way is a generate request with no prompt.
func localContextWindow(ctx context.Context, base, model string) localWindow {
	kind := localKindAt(ctx, base)
	origin := localOrigin(base)
	w := localWindow{kind: kind}
	if origin == "" || model == "" {
		return w
	}
	switch kind.id {
	case "ollama":
		w.trained = ollamaTrainedContext(ctx, origin, model)
		if w.window = ollamaLoadedContext(ctx, origin, model); w.window == 0 {
			pctx, cancel := context.WithTimeout(ctx, localPreloadTimeout)
			load := &http.Client{Transport: localHTTP.Transport, Timeout: localPreloadTimeout}
			var ignored map[string]any
			doLocalJSON(pctx, load, http.MethodPost, origin+"/api/generate",
				map[string]any{"model": model, "keep_alive": "10m"}, &ignored)
			cancel()
			w.window = ollamaLoadedContext(ctx, origin, model)
		}
	case "lmstudio":
		w.window, w.trained = lmstudioContext(ctx, origin, model)
	case "llamacpp":
		var props struct {
			Settings struct {
				NCtx int `json:"n_ctx"`
			} `json:"default_generation_settings"`
		}
		if getLocalJSON(ctx, origin+"/props", &props) {
			w.window = props.Settings.NCtx
		}
	case "vllm":
		var list struct {
			Data []struct {
				ID          string `json:"id"`
				MaxModelLen int    `json:"max_model_len"`
			} `json:"data"`
		}
		if getLocalJSON(ctx, strings.TrimRight(base, "/")+"/models", &list) {
			for _, m := range list.Data {
				if m.ID == model {
					w.window, w.trained = m.MaxModelLen, m.MaxModelLen
				}
			}
		}
	}
	return w
}

// sameOllamaModel compares names the way Ollama does: "qwen2.5-coder" is
// "qwen2.5-coder:latest".
func sameOllamaModel(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		if !strings.Contains(s, ":") {
			s += ":latest"
		}
		return s
	}
	return norm(a) == norm(b)
}

func ollamaLoadedContext(ctx context.Context, origin, model string) int {
	var ps struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if !getLocalJSON(ctx, origin+"/api/ps", &ps) {
		return 0
	}
	for _, m := range ps.Models {
		if sameOllamaModel(m.Name, model) || sameOllamaModel(m.Model, model) {
			return m.ContextLength
		}
	}
	return 0
}

func ollamaTrainedContext(ctx context.Context, origin, model string) int {
	var show struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if !doLocalJSON(ctx, localHTTP, http.MethodPost, origin+"/api/show", map[string]any{"model": model}, &show) {
		return 0
	}
	for k, v := range show.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if n, ok := v.(float64); ok {
				return int(n)
			}
		}
	}
	return 0
}

// lmstudioContext reads LM Studio's own model list: the v1 API names each loaded
// instance's context length, the older v0 only the most the model can take.
func lmstudioContext(ctx context.Context, origin, model string) (window, trained int) {
	var v1 struct {
		Models []struct {
			Key             string `json:"key"`
			MaxContext      int    `json:"max_context_length"`
			LoadedInstances []struct {
				ID     string `json:"id"`
				Config struct {
					ContextLength int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
		} `json:"models"`
	}
	if getLocalJSON(ctx, origin+"/api/v1/models", &v1) && v1.Models != nil {
		for _, m := range v1.Models {
			for _, inst := range m.LoadedInstances {
				if m.Key == model || inst.ID == model {
					return inst.Config.ContextLength, m.MaxContext
				}
			}
			if m.Key == model {
				return 0, m.MaxContext
			}
		}
		return 0, 0
	}
	var v0 struct {
		Data []struct {
			ID         string `json:"id"`
			MaxContext int    `json:"max_context_length"`
		} `json:"data"`
	}
	if getLocalJSON(ctx, origin+"/api/v0/models", &v0) {
		for _, m := range v0.Data {
			if m.ID == model {
				return 0, m.MaxContext
			}
		}
	}
	return 0, 0
}

// localRaiseAdvice is how to give a model more room on each kind of server.
func localRaiseAdvice(kindID string) string {
	switch kindID {
	case "ollama":
		return "start Ollama with a bigger context, for example: OLLAMA_CONTEXT_LENGTH=16384 ollama serve " +
			"(in the Ollama app: Settings, Context length)"
	case "lmstudio":
		return "in LM Studio, load the model again with a bigger Context Length"
	case "llamacpp":
		return "start llama-server with a bigger context, for example: -c 16384"
	case "vllm":
		return "start vLLM with a bigger --max-model-len"
	}
	return ""
}
