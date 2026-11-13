package docker

import (
	"context"
	"errors"
	"net/url"
)

// Exec runs command inside a running container, as the container's own
// user and with its confinement, waits for it to end and returns its exit
// code. There is no way to ask for another user, for privileges or for
// variables. What the command prints is dropped: only a readiness check
// is run this way, and it answers with its exit code.
func (c *Client) Exec(ctx context.Context, container string, command []string) (int, error) {
	create := struct {
		AttachStdout, AttachStderr bool
		Cmd                        []string
	}{true, true, command}
	var made struct {
		ID string `json:"Id"`
	}
	if err := c.post(ctx, "/containers/"+url.PathEscape(container)+"/exec", nil, create, &made); err != nil {
		return 0, err
	}
	// Attached, the call returns when the command has ended.
	start := struct{ Detach, Tty bool }{}
	if err := c.post(ctx, "/exec/"+url.PathEscape(made.ID)+"/start", nil, start, nil); err != nil {
		return 0, err
	}
	var ended struct {
		ExitCode int
		Running  bool
	}
	if err := c.get(ctx, "/exec/"+url.PathEscape(made.ID)+"/json", nil, &ended); err != nil {
		return 0, err
	}
	if ended.Running {
		return 0, errors.New("docker: the command still runs after its output ended")
	}
	return ended.ExitCode, nil
}
