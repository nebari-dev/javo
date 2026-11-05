package apps

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/secrets"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// AgentConfig is what the coding-agent routes need of config.ai.agent.
type AgentConfig struct {
	// Model is the admin's agent model; empty while it is off
	// (config.AICfg.AgentModel).
	Model string
	// Template is the template "Build with pi" starts.
	Template string
}

// AgentStatus says where the caller's agents get their model.
type AgentStatus struct {
	Mode string `json:"mode" enum:"platform,own,login" doc:"platform: the admin's model, through the gateway; own: your key from Settings; login: neither, the agent asks you to sign in"`
	// Model and BaseURL are the model in effect; BaseURL only for own.
	Model   string `json:"model,omitempty"`
	BaseURL string `json:"baseURL,omitempty"`
	// KeyLast4 is the end of your stored key, when you stored one.
	KeyLast4 string `json:"keyLast4,omitempty"`
	// Template is the coding agent template, and Available whether you may use it.
	Template     string `json:"template"`
	TemplateName string `json:"templateName"`
	Available    bool   `json:"available"`
}

type agentStatusOutput struct{ Body AgentStatus }

type agentSettingsInput struct {
	Body struct {
		BaseURL string `json:"baseURL" maxLength:"512" doc:"https address of an OpenAI-compatible API, such as https://openrouter.ai/api/v1"`
		Model   string `json:"model" maxLength:"200" doc:"The model, such as anthropic/claude-sonnet-5.5"`
		APIKey  string `json:"apiKey,omitempty" maxLength:"4096" doc:"Write-only; leave out to keep the stored key"`
	}
}

type agentBuildInput struct {
	Body struct {
		Request string `json:"request" maxLength:"16000" doc:"What to build or do, in your words; the agent gets it as its first prompt"`
	}
}

const agentKeyNote = "Your own AI agent key, set in Settings > AI agent. Only your coding-agent apps get it."

func (s *Workspaces) registerAgent(api huma.API) {
	tag := []string{"ai"}
	huma.Register(api, huma.Operation{
		OperationID: "getAgentStatus", Method: http.MethodGet, Path: "/api/ai/agent",
		Summary: "Where your coding agents get their model", Tags: tag, Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, _ *struct{}) (*agentStatusOutput, error) {
		st, err := s.agentStatus(ctx)
		return &agentStatusOutput{Body: st}, err
	})

	huma.Register(api, huma.Operation{
		OperationID: "setAgentSettings", Method: http.MethodPut, Path: "/api/ai/agent/settings",
		Summary:     "Use your own model and key for your coding agents",
		Description: "Used while the admin gives no agent model. The key is stored encrypted, shown again only as its last four characters, and given only to your coding-agent apps.",
		Tags:        tag, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *agentSettingsInput) (*agentStatusOutput, error) {
		if err := personOnly(ctx); err != nil {
			return nil, err
		}
		b := in.Body
		u := authn.User(ctx)
		if pu, err := url.Parse(b.BaseURL); err != nil || pu.Scheme != "https" || pu.Host == "" || pu.User != nil {
			return nil, problem.FieldError("baseURL", reasons.MsgAgentBaseURLInvalid)
		}
		if m := strings.TrimSpace(b.Model); m == "" || m != b.Model || strings.ContainsFunc(m, unicode.IsControl) {
			return nil, problem.FieldError("model", reasons.MsgAgentModelInvalid)
		}
		prev, err := s.db.AgentSettings().Get(ctx, u.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, problem.Internal("read agent settings", err)
		}
		last4 := prev.KeyLast4
		if b.APIKey == "" {
			if _, err := s.db.Secrets().Get(ctx, u.ID, model.AgentKeySecret); err != nil {
				return nil, problem.FieldError("apiKey", reasons.MsgAgentKeyRequired)
			}
		} else {
			if err := secrets.Put(ctx, s.db, authn.Actor(ctx), u.ID, model.AgentKeySecret, agentKeyNote, b.APIKey); err != nil {
				return nil, err
			}
			last4 = lastRunes(b.APIKey, 4)
		}
		if err := s.db.AgentSettings().Put(ctx, store.AgentSetting{Owner: u.ID, BaseURL: b.BaseURL, Model: b.Model, KeyLast4: last4}); err != nil {
			return nil, problem.Internal("save agent settings", err)
		}
		st, err := s.agentStatus(ctx)
		return &agentStatusOutput{Body: st}, err
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteAgentSettings", Method: http.MethodDelete, Path: "/api/ai/agent/settings",
		Summary: "Stop using your own model and key for your coding agents", Tags: tag, DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		if err := personOnly(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		if err := s.db.AgentSettings().Delete(ctx, u.ID); err != nil {
			return nil, problem.Internal("delete agent settings", err)
		}
		if err := s.db.Secrets().Delete(ctx, authn.Actor(ctx), u.ID, model.AgentKeySecret); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, problem.Internal("delete the agent key", err)
		}
		return nil, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "buildWithAgent", Method: http.MethodPost, Path: "/api/ai/agent/build",
		Summary:     "Start a coding-agent app on a request",
		Description: "Makes a new app of the coding agent template, named after the request, that starts with the request as its first prompt, in the home folder or in the folder the request names when it exists.",
		Tags:        tag, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, s.buildWithAgent)
}

// personOnly refuses an app's or a job's own token (the middleware already
// does): a person, in the browser or with a personal token, makes these
// choices, never a workload.
func personOnly(ctx context.Context) error {
	if authn.NoAppTokens(ctx) != nil {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgAgentBrowserOnly))
	}
	return nil
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return ""
	}
	return string(r[len(r)-n:])
}

func (s *Workspaces) agentStatus(ctx context.Context) (AgentStatus, error) {
	u := authn.User(ctx)
	c := s.cfg.Agent
	st := AgentStatus{Template: c.Template}
	if t, err := templates.Usable(ctx, s.db.Templates(), s.Roles, u, c.Template); err == nil && t.Spec.AgentModel {
		st.Available, st.TemplateName = true, t.Spec.DisplayName
	}
	own, err := s.db.AgentSettings().Get(ctx, u.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return st, problem.Internal("read agent settings", err)
	default:
		if _, kerr := s.db.Secrets().Get(ctx, u.ID, model.AgentKeySecret); kerr != nil {
			own = store.AgentSetting{}
		}
	}
	st.Mode = model.AgentMode(c.Model, own.Owner != "")
	switch st.Mode {
	case model.AgentModePlatform:
		st.Model = c.Model
	case model.AgentModeOwn:
		st.Model, st.BaseURL = own.Model, own.BaseURL
	}
	st.KeyLast4 = own.KeyLast4
	return st, nil
}

// homePath finds a home folder path in a request: ~/a/b or /home/user/a/b.
var homePath = regexp.MustCompile(`(?:^|[\s"'(])(?:~|/home/user)/([^\s"'()]+)`)

// agentFolder is the first folder of the home that request names and that
// exists, relative to the home; "" for none.
func (s *Workspaces) agentFolder(ctx context.Context, owner, request string) string {
	if s.homes == nil {
		return ""
	}
	for _, m := range homePath.FindAllStringSubmatch(request, 3) {
		p, ok := templates.CleanFile(strings.TrimRight(m[1], ".,;:!?/"))
		if !ok || p == "" {
			continue
		}
		parent, name := path.Split(p)
		l, err := s.homes.List(ctx, owner, strings.TrimSuffix(parent, "/"))
		if err != nil {
			continue
		}
		for _, e := range l.Entries {
			if e.Name == name && e.IsDir {
				return p
			}
		}
	}
	return ""
}

// agentDisplayName is the start of request, cut at a word, for the app's name.
func agentDisplayName(request string) string {
	words := strings.Fields(request)
	out := ""
	for _, w := range words {
		if utf8.RuneCountInString(out)+1+utf8.RuneCountInString(w) > 40 {
			break
		}
		out = strings.TrimSpace(out + " " + w)
	}
	if out == "" {
		out = string([]rune(request)[:min(40, utf8.RuneCountInString(request))])
	}
	return out
}

func (s *Workspaces) buildWithAgent(ctx context.Context, in *agentBuildInput) (*workspaceOutput, error) {
	if err := personOnly(ctx); err != nil {
		return nil, err
	}
	u := authn.User(ctx)
	// Control characters are refused; characters nobody sees are dropped.
	req := strings.TrimSpace(in.Body.Request)
	if (&model.App{AgentPrompt: req}).ValidateAgentPrompt() != nil {
		return nil, problem.FieldError("request", reasons.MsgAgentRequestInvalid)
	}
	if req = strings.TrimSpace(model.PrintableText(req)); req == "" {
		return nil, problem.FieldError("request", reasons.MsgAgentRequestInvalid)
	}
	t, err := templates.Usable(ctx, s.db.Templates(), s.Roles, u, s.cfg.Agent.Template)
	if err != nil || !t.Spec.AgentModel {
		return nil, problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgAgentUnavailable))
	}
	base := agentDisplayName(req)
	folder := s.agentFolder(ctx, u.ID, req)
	for n := 1; n <= templates.MaxSampleNames; n++ {
		out, err := s.createFor(ctx, u, appRequest{displayName: templates.SampleName(base, n), template: t.Name, file: folder, agentPrompt: req})
		var taken nameTaken
		if errors.As(err, &taken) {
			continue
		}
		return out, err
	}
	return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgSampleNameTaken))
}
