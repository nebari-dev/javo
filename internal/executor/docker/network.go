package docker

import (
	"context"
	"net/http"
	"net/url"
)

// Network is what the daemon says about one network.
type Network struct {
	ID         string `json:"Id"`
	Name       string
	Driver     string
	Internal   bool
	EnableIPv6 bool
	Labels     map[string]string
	Options    map[string]string
	IPAM       struct {
		Config []struct{ Subnet, Gateway string }
	}
	// Containers are the containers on the network, by id. A list of
	// networks leaves them out.
	Containers map[string]struct{ Name, IPv4Address string }
}

// gatewayMode is the bridge option that, set to isolated on an internal
// network, gives the host no address on the bridge.
const gatewayMode = "com.docker.network.bridge.gateway_mode_ipv4"

// isolated reports whether the network keeps its containers from
// everything but each other: internal (no route out, no forwarding to or
// from other networks), IPv4 only, and with no address of the host on it.
func (n Network) isolated() bool {
	for _, c := range n.IPAM.Config {
		if c.Gateway != "" {
			return false
		}
	}
	return n.Driver == "bridge" && n.Internal && !n.EnableIPv6 && n.Options[gatewayMode] == "isolated"
}

// NetworkCreate makes a network no container can leave: an internal
// bridge with no address of the host on it. There is no other kind to
// ask for. subnet is its address range, or empty for one the daemon
// picks. A name in use is ErrConflict.
func (c *Client) NetworkCreate(ctx context.Context, name, subnet string, labels map[string]string) error {
	type ipam struct {
		Config []map[string]string `json:",omitempty"`
	}
	body := struct {
		Name       string
		Driver     string
		Internal   bool
		EnableIPv6 bool
		Options    map[string]string
		Labels     map[string]string
		IPAM       ipam
	}{
		Name: name, Driver: "bridge", Internal: true, EnableIPv6: false,
		Options: map[string]string{gatewayMode: "isolated"},
		Labels:  c.stamped(labels),
	}
	if subnet != "" {
		body.IPAM.Config = []map[string]string{{"Subnet": subnet}}
	}
	return c.post(ctx, "/networks/create", nil, body, nil)
}

// NetworkInspect reads one network, by name or id.
func (c *Client) NetworkInspect(ctx context.Context, name string) (Network, error) {
	var out Network
	err := c.get(ctx, "/networks/"+url.PathEscape(name), nil, &out)
	return out, err
}

// NetworkList is every network that carries all of labels; without any,
// every network of the daemon.
func (c *Client) NetworkList(ctx context.Context, labels ...string) ([]Network, error) {
	var out []Network
	err := c.get(ctx, "/networks", labelFilter(labels, nil), &out)
	return out, err
}

// NetworkConnect puts a container on a network as well.
func (c *Client) NetworkConnect(ctx context.Context, network, container string) error {
	return c.post(ctx, "/networks/"+url.PathEscape(network)+"/connect", nil, map[string]string{"Container": container}, nil)
}

// NetworkDisconnect takes a container off a network. A container or a
// network that is gone is not an error.
func (c *Client) NetworkDisconnect(ctx context.Context, network, container string) error {
	return gone(c.post(ctx, "/networks/"+url.PathEscape(network)+"/disconnect", nil, map[string]any{"Container": container, "Force": true}, nil))
}

// NetworkRemove removes one of Javo's networks. One that is gone is not an
// error; one that does not carry Javo's label is ErrForeign and stays.
func (c *Client) NetworkRemove(ctx context.Context, name string) error {
	have, err := c.NetworkInspect(ctx, name)
	if err != nil {
		return gone(err)
	}
	if !Ours(have.Labels) {
		return foreign("network", name)
	}
	return gone(c.call(ctx, request{method: http.MethodDelete, path: "/networks/" + url.PathEscape(have.ID)}, nil))
}
