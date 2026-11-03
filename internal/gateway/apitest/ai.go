package apitest

import (
	"context"

	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/ai/provider"
)

// Unlimited is an AI allowance that never runs out.
type Unlimited struct{}

func (Unlimited) Reserve(_ context.Context, _ string, _ int) (func(int), error) {
	return func(int) {}, nil
}

func AiService(p provider.Provider) *ai.Service {
	return &ai.Service{Provider: p, Model: "test-model", Budget: Unlimited{}, MaxInputRunes: 20000}
}

type ExplainJSON struct {
	Explanation *struct {
		Summary  string
		Evidence []string
		Actions  []struct{ Action, Target string }
	}
	Proposals  []struct{ Action, Target, Summary string }
	Bundle     struct{ Text string }
	Model      string
	Message    string
	RetryAfter int
}

const RestartAnswer = `{"summary":"The app stopped with an error.","cause":"An error at start.","confidence":"medium","fix":"Restart it.",
 "evidence":["condition/Started"],"actions":[{"action":"restart","target":"sales","size":""},{"action":"stop","target":"bobs-app","size":""}]}`

type DraftJSON struct {
	Draft   struct{ DisplayName, Template, File string }
	SetBy   []string
	Errors  []struct{ Location, Message string }
	Message string
}

type CommandDraftJSON struct {
	Reply string
	Plans []PlanJSON
	Draft *DraftJSON
}
