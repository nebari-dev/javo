package model

// Where a coding-agent app's model comes from (TemplateSpec.AgentModel), in
// order of precedence: the admin's model through the gateway, the owner's own
// key, or none, and the agent asks its owner to sign in (pi's /login).
const (
	AgentModePlatform = "platform"
	AgentModeOwn      = "own"
	AgentModeLogin    = "login"
)

// AgentKeySecret is the name of the secret that holds a person's own agent
// key. JAVO_ names are the platform's, so a person cannot make or bind one
// by hand; only Settings > AI agent writes it and only their agent apps get it.
const AgentKeySecret = ProductEnvPrefix + "AGENT_KEY"

// AgentMode is where the agents of a person get their model: platform while
// the admin's agent model is on (platformModel not empty), own while the
// person stored a key, login otherwise.
func AgentMode(platformModel string, ownKey bool) string {
	switch {
	case platformModel != "":
		return AgentModePlatform
	case ownKey:
		return AgentModeOwn
	}
	return AgentModeLogin
}
