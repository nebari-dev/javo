package docker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Volume is what the daemon says about one volume.
type Volume struct {
	Name, Driver string
	Labels       map[string]string
	// Options are how the volume is mounted; none for a plain one.
	Options map[string]string
}

// VolumeCreate makes a plain volume on the host's disk, or finds the one
// of that name. A fresh one belongs to root (Own). There is no way to ask
// for driver options here: they can turn a volume into a path of the
// host. A volume of that name that is not Javo's is ErrForeign.
func (c *Client) VolumeCreate(ctx context.Context, name string, labels map[string]string) error {
	return c.volumeCreate(ctx, name, labels, nil)
}

// MemoryVolumeCreate makes a volume that lives in memory only and holds
// at most megabytes: nothing written to it reaches the host's disk (but
// for swap, when the host has any). Its root belongs to uid:gid and
// nobody else may enter. It is empty again whenever no running container
// mounts it.
func (c *Client) MemoryVolumeCreate(ctx context.Context, name string, uid, gid int64, megabytes int, labels map[string]string) error {
	return c.volumeCreate(ctx, name, labels, map[string]string{
		"type": "tmpfs", "device": "tmpfs",
		"o": fmt.Sprintf("size=%dm,mode=0700,uid=%d,gid=%d,noexec,nosuid,nodev", megabytes, uid, gid),
	})
}

func (c *Client) volumeCreate(ctx context.Context, name string, labels, opts map[string]string) error {
	body := struct {
		Name, Driver string
		Labels       map[string]string
		DriverOpts   map[string]string `json:",omitempty"`
	}{Name: name, Driver: "local", Labels: c.stamped(labels), DriverOpts: opts}
	// The daemon answers with the volume of that name, new or not.
	var have Volume
	if err := c.post(ctx, "/volumes/create", nil, body, &have); err != nil {
		return err
	}
	if !Ours(have.Labels) {
		return foreign("volume", name)
	}
	return nil
}

// VolumeInspect reads one volume.
func (c *Client) VolumeInspect(ctx context.Context, name string) (Volume, error) {
	var out Volume
	err := c.get(ctx, "/volumes/"+url.PathEscape(name), nil, &out)
	return out, err
}

// VolumeList is every volume that carries all of labels.
func (c *Client) VolumeList(ctx context.Context, labels ...string) ([]Volume, error) {
	var out struct{ Volumes []Volume }
	err := c.get(ctx, "/volumes", labelFilter(labels, nil), &out)
	return out.Volumes, err
}

// VolumeRemove removes one of Javo's volumes and what it holds. One that
// is gone is not an error; one that does not carry Javo's label is
// ErrForeign and stays; one a container still uses is ErrConflict.
func (c *Client) VolumeRemove(ctx context.Context, name string) error {
	have, err := c.VolumeInspect(ctx, name)
	if err != nil {
		return gone(err)
	}
	if !Ours(have.Labels) {
		return foreign("volume", name)
	}
	return gone(c.call(ctx, request{method: http.MethodDelete, path: "/volumes/" + url.PathEscape(name)}, nil))
}
