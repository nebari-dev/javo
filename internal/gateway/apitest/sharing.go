package apitest

import (
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

var (
	AliceID = PersonID("alice")
	BobID   = PersonID("bob")
	CarolID = PersonID("carol")
	DaveID  = PersonID("dave")
)

var SeparateHosts = config.URLs{UIHost: "javo.example.com", AppsHost: "apps.example.com", Scheme: "https"}

func SharableTemplate(max string) *model.Template {
	t := gatewaytest.Template("streamlit", "streamlit")
	t.Spec.MaxSharing = max
	return t
}

type SharingJSON struct {
	Access    string
	MaxAccess string
	Users     []struct {
		ID, DisplayName, Email, Note string
		Active                       bool
	}
	Groups  []struct{ Value, Label string }
	Link    string
	Levels  []struct{ Value, Label string }
	TooOpen string
}

type SharedJSON struct {
	Empty string
	Items []struct {
		Name, DisplayName, URL, Via string
		Owner                       struct{ ID, DisplayName string }
	}
}

const SharePath = "/api/workspaces/report/sharing"

func NewSharingFixture(t *testing.T, o Options, extra ...any) *Fixture {
	t.Helper()
	now := time.Now()
	objs := append([]any{
		SharableTemplate(model.AccessSignedIn), gatewaytest.Workspace("report", AliceID, "Report"),
	}, extra...)
	o.People = append([]TestPerson{SignedIn("alice", now), SignedIn("bob", now), SignedIn("carol", now)}, o.People...)
	return New(t, o, objs...)
}

type DirectoryJSON struct {
	People []struct{ ID, DisplayName, Email string }
	Groups []struct{ Value string }
}
