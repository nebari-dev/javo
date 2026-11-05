// Package agentproxy is the model endpoint coding-agent apps call: an
// OpenAI-compatible chat completions route that forwards each request to the
// admin's provider with the admin's key, on the admin's model. Spend is
// capped by the admin at the provider (such as an OpenRouter key limit), not
// here. The key stays in the gateway; the agent
// authenticates with its app's own API token. Prompts and answers pass
// through and are never logged.
package agentproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Route is where the endpoint is served; an agent's base URL is
// $JAVO_URL + Route.
const Route = "/api/ai/openai/v1"

// DefaultMaxOutput is the most output a request may ask for.
const DefaultMaxOutput = 32000

// maxAnswerBytes bounds an answer that is not streamed.
const maxAnswerBytes = 32 << 20

// Proxy forwards agents' requests to the admin's provider.
type Proxy struct {
	// BaseURL is the provider's OpenAI-compatible base, such as
	// https://openrouter.ai/api/v1; empty is OpenAI's.
	BaseURL string
	APIKey  string
	// Model replaces any model a request names that Allows refuses.
	Model  string
	Allows func(model string) bool
	// Models are listed by GET /models.
	Models []string
	// MaxRequestBytes caps a request body.
	MaxRequestBytes int64
	// Caller is who the request comes from, and whether they may use the
	// endpoint at all.
	Caller func(ctx context.Context) (person string, ok bool)
	// Client sends the requests; nil is http.DefaultClient.
	Client *http.Client
}

// Register adds the routes to mux.
func (p *Proxy) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+Route+"/chat/completions", p.chat)
	mux.HandleFunc("GET "+Route+"/models", p.models)
}

// fail answers in the OpenAI error shape, which agents show as it is.
func fail(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": kind}})
}

const notAllowed = "Only a coding-agent app's own token may use this model endpoint."

func (p *Proxy) models(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.Caller(r.Context()); !ok {
		fail(w, http.StatusForbidden, "permission_denied", notAllowed)
		return
	}
	data := []map[string]any{}
	for _, m := range p.Models {
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "javo"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (p *Proxy) chat(w http.ResponseWriter, r *http.Request) {
	person, ok := p.Caller(r.Context())
	if !ok {
		fail(w, http.StatusForbidden, "permission_denied", notAllowed)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.MaxRequestBytes))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		fail(w, http.StatusRequestEntityTooLarge, "invalid_request_error", fmt.Sprintf("The request is larger than the %d bytes allowed.", p.MaxRequestBytes))
		return
	case err != nil:
		fail(w, http.StatusBadRequest, "invalid_request_error", "The request could not be read.")
		return
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", "The request is not a JSON object.")
		return
	}
	body, model, stream := p.rewrite(body)
	out, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.base()+"/chat/completions", bytes.NewReader(out))
	if err != nil {
		fail(w, http.StatusInternalServerError, "server_error", "The request could not be made.")
		return
	}
	// Only these headers go out: never the caller's token or cookies.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		// The error names the URL, never the key or the body.
		slog.Warn("agent model call failed", "user", person, "err", err)
		audit(person, model, 0, 0, "unavailable")
		fail(w, http.StatusBadGateway, "server_error", "The AI provider could not be reached.")
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	var in, outTok int
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		in, outTok, _ = copyEvents(w, resp.Body)
	} else {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
		_, _ = w.Write(b)
		in, outTok, _ = usageOf(b)
	}
	result := "ok"
	if resp.StatusCode >= 400 {
		result = fmt.Sprintf("status %d", resp.StatusCode)
	}
	audit(person, model, in, outTok, result)
}

// forwarded are the OpenAI chat completions fields that go to the provider;
// any other field (n, a provider's routing or plugin fields) is dropped, so
// a request cannot ask for several answers or another model. n is left out,
// which is one answer.
var forwarded = []string{
	"messages", "model", "stream", "stream_options", "temperature", "top_p",
	"max_tokens", "max_completion_tokens", "stop", "tools", "tool_choice",
	"response_format", "presence_penalty", "frequency_penalty", "seed", "user",
	"parallel_tool_calls",
}

// tokenFields are the fields an output limit is asked for in.
var tokenFields = []string{"max_tokens", "max_completion_tokens"}

// rewrite builds the provider's request from in: only the forwarded
// fields, the admin's model unless in names one Allows, an output limit of
// at most DefaultMaxOutput (DefaultMaxOutput when in asks for none), and a
// streamed answer asked to report its usage (for the audit line).
func (p *Proxy) rewrite(in map[string]json.RawMessage) (out map[string]json.RawMessage, model string, stream bool) {
	out = map[string]json.RawMessage{}
	for _, f := range forwarded {
		if v, ok := in[f]; ok {
			out[f] = v
		}
	}
	_ = json.Unmarshal(out["model"], &model)
	if !p.Allows(model) {
		model = p.Model
		out["model"], _ = json.Marshal(model)
	}
	limit := DefaultMaxOutput
	named := []string{}
	for _, f := range tokenFields {
		if _, ok := out[f]; !ok {
			continue
		}
		named = append(named, f)
		if n, ok := tokenLimit(out[f]); ok && n < limit {
			limit = n
		}
	}
	if len(named) == 0 {
		named = tokenFields[:1]
	}
	for _, f := range named {
		out[f], _ = json.Marshal(limit)
	}
	delete(out, "stream_options")
	if json.Unmarshal(out["stream"], &stream) == nil && stream {
		out["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	return out, model, stream
}

// tokenLimit reads an output limit written as a JSON number in any form
// (500, 5e2, 500.0) or as a string holding one, rounded down; ok is false
// for anything else, or for a limit below one.
func tokenLimit(raw json.RawMessage) (n int, ok bool) {
	text := strings.TrimSpace(string(raw))
	var s string
	if json.Unmarshal(raw, &s) == nil {
		text = strings.TrimSpace(s)
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || f < 1 {
		return 0, false
	}
	if f >= DefaultMaxOutput {
		return DefaultMaxOutput, true
	}
	return int(f), true
}

func (p *Proxy) base() string {
	if p.BaseURL == "" {
		return "https://api.openai.com/v1"
	}
	return strings.TrimRight(p.BaseURL, "/")
}

func (p *Proxy) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// copyEvents passes a server-sent event stream through line by line,
// flushing each event, and reads the usage the last events report.
func copyEvents(w http.ResponseWriter, r io.Reader) (in, out int, found bool) {
	f, _ := w.(http.Flusher)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if i, o, ok := usageOf(bytes.TrimSpace(data)); ok {
				in, out, found = i, o, true
			}
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return in, out, found
		}
		if len(line) == 0 && f != nil {
			f.Flush()
		}
	}
	if f != nil {
		f.Flush()
	}
	return in, out, found
}

// usageOf reads the usage of an answer or an event.
func usageOf(b []byte) (in, out int, found bool) {
	var v struct {
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &v) != nil || v.Usage == nil {
		return 0, 0, false
	}
	return v.Usage.Prompt, v.Usage.Completion, true
}

// audit writes one line per call: who, the model, the tokens and the
// outcome. Never the prompt or the answer.
func audit(person, model string, in, out int, result string) {
	slog.Info("ai.call", "user", person, "feature", "agent", "model", model, "tokens_in", in, "tokens_out", out, "outcome", result)
}
