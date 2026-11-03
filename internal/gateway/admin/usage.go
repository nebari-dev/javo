package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/usage"
)

// UsageRow is the use of one person or group over the window.
type UsageRow struct {
	Key         string  `json:"key" doc:"Identity name, or the group; empty for people in no group"`
	DisplayName string  `json:"displayName"`
	Hours       float64 `json:"hours" doc:"App-hours run in the window"`
	Apps        int     `json:"apps"`
	People      int     `json:"people"`
	RunningNow  int     `json:"runningNow"`
}

type usageInput struct {
	By   string `query:"by" enum:"person,group" default:"person"`
	Days int    `query:"days" minimum:"1" maximum:"366" default:"7"`
}

type usageOutput struct {
	Body struct {
		Since  time.Time `json:"since"`
		Totals struct {
			Hours      float64 `json:"hours"`
			People     int     `json:"people"`
			RunningNow int     `json:"runningNow"`
		} `json:"totals"`
		Items []UsageRow `json:"items"`
	}
}

func (a *API) registerUsage(api huma.API) {
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminUsage", Method: http.MethodGet, Path: "/api/admin/usage",
		Summary: "How long apps ran in the last days, per person or group (platform admins)",
	}, func(ctx context.Context, in *usageInput) (*usageOutput, error) {
		now := a.Now()
		since := now.Add(-time.Duration(in.Days) * 24 * time.Hour)
		ivs, err := a.DB.Usage().Since(ctx, since)
		if err != nil {
			return nil, problem.Internal("read usage", err)
		}
		people := map[string]usage.Person{}
		for _, i := range ivs {
			if _, ok := people[i.Owner]; ok {
				continue
			}
			var p usage.Person
			// A person deleted since keeps their hours under their identity name.
			if row, err := a.People.ByName(ctx, i.Owner); err == nil {
				p = usage.Person{DisplayName: row.DisplayName, Groups: row.Groups}
			}
			people[i.Owner] = p
		}
		rows, totals := usage.Aggregate(ivs, people, usage.GroupBy(in.By), since, now)
		out := &usageOutput{}
		out.Body.Since = since
		out.Body.Totals.Hours, out.Body.Totals.People, out.Body.Totals.RunningNow = totals.Hours, totals.People, totals.RunningNow
		out.Body.Items = make([]UsageRow, len(rows))
		for i, r := range rows {
			out.Body.Items[i] = UsageRow{Key: r.Key, DisplayName: r.DisplayName, Hours: r.Hours, Apps: r.Apps, People: r.People, RunningNow: r.RunningNow}
		}
		return out, nil
	})
}
