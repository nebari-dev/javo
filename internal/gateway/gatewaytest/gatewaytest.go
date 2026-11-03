// Package gatewaytest holds the test fixtures the gateway's packages share:
// rows to seed a test database with, and fake app backends.
package gatewaytest

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/config"
	exposurepath "github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"io"
	"net/http"
	"strings"
	"time"
)

// Actor makes the seed rows' audit entries.
var Actor = model.Actor{System: "test"}

// WithRuns is a job to seed together with the runs the controller recorded
// for it, newest last.
type WithRuns struct {
	Job  *model.Job
	Runs []model.Run
}

// Seed writes apps, templates and jobs into the test database. An app or job
// with a status gets it recorded as observed at its first version, as the
// controller would.
func Seed(t *testing.T, db *store.Store, objs []any) {
	t.Helper()
	ctx := context.Background()
	for _, obj := range objs {
		switch o := obj.(type) {
		case *model.Template:
			tpl := *o
			if tpl.Source == "" {
				tpl.Source = store.SourceAdmin
			}
			tpl.Spec.ApplyDefaults()
			if _, err := db.Templates().Put(ctx, Actor, tpl, 0); err != nil {
				t.Fatalf("seed template %s: %v", tpl.Name, err)
			}
		case *model.App:
			created, err := db.Apps().Create(ctx, Actor, o)
			if err != nil {
				t.Fatalf("seed app %s: %v", o.Name, err)
			}
			if o.Status.Phase != "" || len(o.Status.Conditions) > 0 {
				if err := db.Apps().SetStatus(ctx, created.ID, created.Version, o.Status); err != nil {
					t.Fatal(err)
				}
			}
		case *model.Home:
			h, err := db.Homes().Ensure(ctx, o.Namespace, o.Spec.Owner, o.Spec.Size)
			if err != nil {
				t.Fatalf("seed home %s: %v", o.Spec.Owner, err)
			}
			h.Status = o.Status
			if _, err := db.Homes().Update(ctx, h, h.Version); err != nil {
				t.Fatal(err)
			}
		case *model.SharedFolder:
			f, err := db.SharedFolders().Create(ctx, Actor, *o)
			if err != nil {
				t.Fatalf("seed folder %s: %v", o.Name, err)
			}
			f.Status = o.Status
			if _, err := db.SharedFolders().Update(ctx, f, f.Version); err != nil {
				t.Fatal(err)
			}
		case *model.ClusterStorage:
			// The findings as the controller leaves them, and the admin's
			// requests (a probe, a schedule) as stored.
			if err := db.Storage().SetClusterStatus(ctx, o.Status); err != nil {
				t.Fatal(err)
			}
			if err := db.Storage().SetProbeStatus(ctx, o.Status.Probe, o.Status.ProbeResults); err != nil {
				t.Fatal(err)
			}
			if err := db.Storage().UpdateRequests(ctx, Actor, "storage.seed", func(r *model.StorageRequests) error {
				r.Probe, r.Migration = o.Spec.Probe, o.Spec.Migration
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		case *model.Job:
			seedJob(t, db, WithRuns{Job: o})

		case WithRuns:
			seedJob(t, db, o)
		}
	}
}

func seedJob(t *testing.T, db *store.Store, j WithRuns) {
	t.Helper()
	ctx := context.Background()
	created, err := db.Jobs().Create(ctx, Actor, j.Job)
	if err != nil {
		t.Fatalf("seed job %s: %v", j.Job.Name, err)
	}
	if j.Job.Status.Phase != "" || len(j.Job.Status.Conditions) > 0 || len(j.Job.Status.NextRunTimes) > 0 || !j.Job.Status.LastScheduleTime.IsZero() {
		if err := db.Jobs().SetStatus(ctx, created.ID, created.Version, j.Job.Status); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range j.Runs {
		r.JobID = created.ID
		if _, err := db.Jobs().RecordRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
}

// App is an app as the API stores it; short is the short name.
func App(owner, short string) *model.App {
	return &model.App{Namespace: Namespace, Name: short, Owner: owner, Template: "t", DisplayName: short}
}

// Workspace is an app of the streamlit template with a display name.
func Workspace(name, owner, display string) *model.App {
	app := App(owner, name)
	app.DisplayName, app.Template = display, "streamlit"
	return app
}

// Template is a template of kind whose apps have a login of their own.
func Template(name, kind string, groups ...string) *model.Template {
	var exts []string
	if kind == "streamlit" {
		exts = []string{".py"}
	}
	return &model.Template{
		Name: name,
		Spec: model.TemplateSpec{
			DisplayName: name, Kind: kind, Image: "example.org/" + name + ":1", Port: 8501, AllowedGroups: groups,
			FileExtensions: exts,
			// A login of its own: tests about apps without one say so.
			AppLogin: "Enabled",
		},
	}
}

// Namespace is where test apps live.
const Namespace = "javo-apps"

// OneHostExposure is the M1 layout: apps under /u/ on the UI host.
var OneHostExposure = exposurepath.New(config.URLs{})

// EchoBackend completes a websocket-style upgrade and echoes raw bytes.
func EchoBackend(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "no upgrade", 400)
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	brw.Flush()
	io.Copy(conn, brw)
}

// Flushed is what one flush of tr writes, by app id.
func Flushed(t *testing.T, tr *activity.Tracker) map[string]time.Time {
	t.Helper()
	w := &activityWrites{}
	if err := tr.Flush(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	return w.seen
}

type activityWrites struct{ seen map[string]time.Time }

func (w *activityWrites) NoteActivity(_ context.Context, seen map[string]time.Time) error {
	w.seen = seen
	return nil
}
