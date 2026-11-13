package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

// Event is one change the daemon reports for a container.
type Event struct {
	// Action is what happened: create, start, die, destroy, oom and others.
	Action string
	Actor  struct {
		// ID is the container's id; Attributes hold its labels, its name
		// and, for die, its exitCode.
		ID         string
		Attributes map[string]string
	}
	TimeNano int64 `json:"timeNano"`
}

// Events calls fn with each change to a container that carries all of
// labels, from now on, until ctx ends. It returns ctx's error then, and
// another when the daemon ends the stream first: the caller starts again
// and looks at everything once, since changes in between were missed.
func (c *Client) Events(ctx context.Context, labels []string, fn func(Event)) error {
	resp, err := c.do(ctx, request{method: "GET", path: "/events", query: labelFilter(labels, map[string][]string{"type": {"container"}})})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				return errors.New("docker: the daemon ended the event stream")
			}
			return err
		}
		fn(e)
	}
}
