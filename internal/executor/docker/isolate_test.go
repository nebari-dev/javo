package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/workload"
)

var ref = workload.Ref{Namespace: "javo-alice", Name: "notebook", ID: "app-1", Owner: "alice"}

// networks is a fake daemon that keeps networks. ignoreIsolation makes it
// behave like one that does not know the isolated gateway mode: it gives
// the host an address on every bridge.
type networks struct {
	calls
	have            map[string]map[string]any
	ignoreIsolation bool
}

func (n *networks) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.add(r)
	p := strings.TrimPrefix(r.URL.Path, "/v"+MinAPI+"/networks")
	name := strings.Trim(strings.TrimSuffix(strings.TrimSuffix(p, "/connect"), "/disconnect"), "/")
	switch {
	case r.Method == "POST" && p == "/create":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["Id"] = "id-" + body["Name"].(string)
		if n.ignoreIsolation {
			body["IPAM"] = map[string]any{"Config": []map[string]string{{"Subnet": "172.30.0.0/16", "Gateway": "172.30.0.1"}}}
		}
		body["Containers"] = map[string]any{}
		n.have[body["Name"].(string)] = body
		reply(w, 201, map[string]string{"Id": body["Id"].(string)})
	case r.Method == "GET" && p == "":
		list := []map[string]any{}
		for _, v := range n.have {
			list = append(list, v)
		}
		reply(w, 200, list)
	case n.have[name] == nil && n.have[strings.TrimPrefix(name, "id-")] == nil:
		fail(w, 404, "network "+name+" not found")
	case r.Method == "GET":
		reply(w, 200, n.have[name])
	case r.Method == "DELETE":
		delete(n.have, strings.TrimPrefix(name, "id-"))
		w.WriteHeader(204)
	case strings.HasSuffix(p, "/connect"):
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		n.have[name]["Containers"].(map[string]any)["id-gw"] = map[string]string{"Name": body["Container"]}
	}
}

func TestIsolateMakesAnInternalNetworkWithTheGatewayOnIt(t *testing.T) {
	d := &networks{have: map[string]map[string]any{
		"other": {"Name": "other", "IPAM": map[string]any{"Config": []map[string]string{{"Subnet": "10.200.0.0/27"}}}},
	}}
	c := fake(t, d.ServeHTTP)
	ctx := context.Background()
	pool := netip.MustParsePrefix("10.200.0.0/16")
	if err := Isolate(ctx, c, ref, "javo-gateway", pool); err != nil {
		t.Fatal(err)
	}
	n := d.have["javo-javo-alice.notebook"]
	if n == nil || n["Internal"] != true || n["Driver"] != "bridge" || n["EnableIPv6"] != false {
		t.Fatalf("network = %v", n)
	}
	if got := n["Options"].(map[string]any)[gatewayMode]; got != "isolated" {
		t.Errorf("gateway mode = %v", got)
	}
	// The first two ranges of the pool overlap a network that exists.
	if got := n["IPAM"].(map[string]any)["Config"].([]any)[0].(map[string]any)["Subnet"]; got != "10.200.0.32/28" {
		t.Errorf("subnet = %v", got)
	}
	labels := n["Labels"].(map[string]any)
	if labels[LabelManaged] != "true" || labels[LabelName] != "notebook" || labels[LabelNamespace] != "javo-alice" {
		t.Errorf("labels = %v", labels)
	}
	if _, ok := n["Containers"].(map[string]any)["id-gw"]; !ok {
		t.Error("the gateway is not on the network")
	}

	// A second call finds everything in place and changes nothing.
	before := len(d.all())
	if err := Isolate(ctx, c, ref, "javo-gateway", pool); err != nil {
		t.Fatal(err)
	}
	for _, call := range d.all()[before:] {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("second Isolate called %s", call)
		}
	}
}

// A daemon that does not know the isolated gateway mode puts the host on
// the bridge. No workload may start on such a network.
func TestIsolateRefusesANetworkTheHostIsOn(t *testing.T) {
	d := &networks{have: map[string]map[string]any{}, ignoreIsolation: true}
	c := fake(t, d.ServeHTTP)
	err := Isolate(context.Background(), c, ref, "javo-gateway", netip.Prefix{})
	if err == nil || !strings.Contains(err.Error(), "not isolated") {
		t.Fatalf("err = %v", err)
	}
	if len(d.have) != 0 {
		t.Errorf("the network was kept: %v", d.have)
	}
	for _, call := range d.all() {
		if strings.HasSuffix(call, "/connect") {
			t.Error("the gateway was connected to it")
		}
	}
}

func TestANetworkThatIsNotJavosIsLeftAlone(t *testing.T) {
	d := &networks{have: map[string]map[string]any{
		"javo-javo-alice.notebook": {"Name": "javo-javo-alice.notebook", "Id": "id-x", "Internal": true, "Labels": map[string]string{}},
	}}
	c := fake(t, d.ServeHTTP)
	ctx := context.Background()
	if err := Isolate(ctx, c, ref, "javo-gateway", netip.Prefix{}); !errors.Is(err, ErrForeign) {
		t.Fatalf("Isolate: %v", err)
	}
	if err := Release(ctx, c, ref, "javo-gateway"); !errors.Is(err, ErrForeign) {
		t.Fatalf("Release: %v", err)
	}
	if err := c.NetworkRemove(ctx, "javo-javo-alice.notebook"); !errors.Is(err, ErrForeign) {
		t.Fatalf("NetworkRemove: %v", err)
	}
	for _, call := range d.all() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("called %s", call)
		}
	}
}

func TestReleaseRemovesTheNetworkAndToleratesNone(t *testing.T) {
	d := &networks{have: map[string]map[string]any{}}
	c := fake(t, d.ServeHTTP)
	ctx := context.Background()
	if err := Release(ctx, c, ref, "javo-gateway"); err != nil {
		t.Fatalf("nothing to release: %v", err)
	}
	if err := Isolate(ctx, c, ref, "javo-gateway", netip.Prefix{}); err != nil {
		t.Fatal(err)
	}
	if err := Release(ctx, c, ref, "javo-gateway"); err != nil {
		t.Fatal(err)
	}
	if len(d.have) != 0 {
		t.Errorf("networks left: %v", d.have)
	}
}

func TestFreeSubnet(t *testing.T) {
	taken := func(subnets ...string) []Network {
		var out []Network
		for _, s := range subnets {
			var n Network
			n.IPAM.Config = []struct{ Subnet, Gateway string }{{Subnet: s}}
			out = append(out, n)
		}
		return out
	}
	pool := netip.MustParsePrefix("10.200.0.0/27")
	if got, err := freeSubnet(pool, nil); err != nil || got.String() != "10.200.0.0/28" {
		t.Errorf("empty: %v %v", got, err)
	}
	if got, err := freeSubnet(pool, taken("10.200.0.0/28", "192.168.0.0/16")); err != nil || got.String() != "10.200.0.16/28" {
		t.Errorf("one taken: %v %v", got, err)
	}
	if _, err := freeSubnet(pool, taken("10.0.0.0/8")); err == nil {
		t.Error("a full pool gave a range")
	}
	for _, bad := range []string{"10.200.0.0/29", "fd00::/64"} {
		if _, err := freeSubnet(netip.MustParsePrefix(bad), nil); err == nil {
			t.Errorf("pool %s was accepted", bad)
		}
	}
}
