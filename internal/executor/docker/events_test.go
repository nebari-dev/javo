package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestEventsOfLabelledContainersAreDelivered(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filters") != `{"label":["javo.managed=true"],"type":["container"]}` {
			fail(w, 400, "filters "+r.URL.Query().Get("filters"))
			return
		}
		enc := json.NewEncoder(w)
		for _, action := range []string{"start", "die"} {
			enc.Encode(map[string]any{"Type": "container", "Action": action, "Actor": map[string]any{
				"ID": "abc", "Attributes": map[string]string{LabelName: "notebook", "exitCode": "3"}}})
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	var got []Event
	err := c.Events(ctx, []string{Managed}, func(e Event) {
		got = append(got, e)
		if len(got) == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got[0].Action != "start" || got[1].Action != "die" || got[1].Actor.ID != "abc" || got[1].Actor.Attributes["exitCode"] != "3" {
		t.Fatalf("events = %+v", got)
	}
}

// A stream the daemon ends is an error, so the caller looks again at
// everything it may have missed.
func TestEventsReportAStreamTheDaemonEnded(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {})
	err := c.Events(context.Background(), nil, func(Event) {})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
