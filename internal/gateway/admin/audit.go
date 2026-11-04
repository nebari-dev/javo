package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
)

// AuditEvent is one row of the audit log.
type AuditEvent struct {
	ID              string          `json:"id"`
	At              time.Time       `json:"at"`
	Actor           string          `json:"actor" doc:"Identity name, or the system component"`
	Via             string          `json:"via" doc:"session, token:<id> or system"`
	Action          string          `json:"action"`
	ObjectKind      string          `json:"objectKind"`
	ObjectName      string          `json:"objectName"`
	ObjectID        string          `json:"objectId,omitempty"`
	ObjectNamespace string          `json:"objectNamespace,omitempty"`
	ObjectOwner     string          `json:"objectOwner,omitempty"`
	Allowed         bool            `json:"allowed"`
	Detail          json.RawMessage `json:"detail,omitempty"`
}

type auditInput struct {
	Limit int `query:"limit" minimum:"1" maximum:"1000" default:"100" doc:"How many of the newest events"`
}

type auditOutput struct {
	Body struct {
		Items []AuditEvent `json:"items"`
	}
}

func (a *API) registerAudit(api huma.API) {
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminAuditLog", Method: http.MethodGet, Path: "/api/admin/audit",
		Summary: "The newest events of the audit log (platform admins)",
	}, func(ctx context.Context, in *auditInput) (*auditOutput, error) {
		events, err := a.DB.Audit().List(ctx, in.Limit)
		if err != nil {
			return nil, problem.Internal("read the audit log", err)
		}
		out := &auditOutput{}
		out.Body.Items = make([]AuditEvent, len(events))
		for i, e := range events {
			detail := e.Detail
			if len(detail) == 0 || string(detail) == "{}" {
				detail = nil
			}
			out.Body.Items[i] = AuditEvent{
				ID: e.ID, At: e.At, Actor: e.Actor, Via: e.Via, Action: e.Action, ObjectKind: e.ObjectKind,
				ObjectName: e.ObjectName, ObjectID: e.ObjectID, ObjectNamespace: e.ObjectNamespace,
				ObjectOwner: e.ObjectOwner, Allowed: e.Allowed, Detail: detail,
			}
		}
		return out, nil
	})
}
