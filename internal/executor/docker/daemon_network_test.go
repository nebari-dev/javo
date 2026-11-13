package docker

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/workload"
)

// testPool is where the networks of the daemon tests take their
// addresses, and onePool has room for one network. Each test process
// picks its own ranges (one of 128 pairs), so that two of them seldom
// compete for the same addresses.
var testPool, onePool = func() (netip.Prefix, netip.Prefix) {
	n := int(testRun[0]+testRun[1]) % 128 * 2
	return netip.MustParsePrefix(fmt.Sprintf("10.214.%d.0/24", n)), netip.MustParsePrefix(fmt.Sprintf("10.214.%d.16/28", n+1))
}()

// What a workload on its own network reaches: the units beside it on
// loopback and the gateway, and nothing else. Not another workload, not
// the host, not the internet, not the cloud's metadata address, and no
// outside name.
func TestDaemonAWorkloadReachesOnlyItselfAndTheGateway(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	a, b := testSpec("net-a"), testSpec("net-b")
	sandboxB, _ := SandboxName(b.Ref)

	// The gateway keeps trying to fetch workload b's port, as Javo's
	// proxy would.
	gw := gateway(t, c, "net-gw", "until wget -q -T 2 -O - http://"+sandboxB+":8080/; do sleep 0.3; done")
	sandbox(t, c, a, gw)
	idB := sandbox(t, c, b, gw)
	start(t, c, b, sh("app", "mkdir /tmp/w; echo from-b > /tmp/w/index.html; httpd -f -p 8080 -h /tmp/w"))

	inspectB, err := c.ContainerInspect(ctx, idB)
	if err != nil {
		t.Fatal(err)
	}
	netB, _ := NetworkName(b.Ref)
	ipB := inspectB.IP(netB)
	if ipB == "" {
		t.Fatalf("workload b has no address on %s: %+v", netB, inspectB.NetworkSettings)
	}
	inspectGW, err := c.ContainerInspect(ctx, gw)
	if err != nil {
		t.Fatal(err)
	}
	gwName := strings.TrimPrefix(inspectGW.Name, "/")
	// The host's address on the daemon's default network.
	bridge, err := c.NetworkInspect(ctx, "bridge")
	if err != nil || len(bridge.IPAM.Config) == 0 || bridge.IPAM.Config[0].Gateway == "" {
		t.Fatalf("default bridge: %+v, %v", bridge, err)
	}
	host := bridge.IPAM.Config[0].Gateway

	// A helper of workload a listens on loopback only, as the agent
	// helper does.
	helper := sh("helper", "mkdir /tmp/w; echo from-helper > /tmp/w/index.html; httpd -f -p 127.0.0.1:7070 -h /tmp/w")
	helper.Stays = true
	start(t, c, a, helper)

	fetch := func(name, url string) string {
		return `echo "` + name + `=$(wget -q -T 3 -O - ` + url + ` 2>&1 | head -c 200 | tr '\n' ' ')";`
	}
	code, out := run(t, c, a, sh("probe", `
for i in 1 2 3 4 5 6 7 8 9 10; do wget -q -T 1 -O /dev/null http://127.0.0.1:7070/ && break; sleep 0.3; done
`+fetch("loopback", "http://127.0.0.1:7070/")+
		fetch("gateway", "http://"+gwName+":9090/")+
		fetch("other", "http://"+ipB+":8080/")+
		fetch("othername", "http://"+sandboxB+":8080/")+
		fetch("host", "http://"+host+":22/")+
		fetch("internet", "http://1.1.1.1/")+
		fetch("metadata", "http://169.254.169.254/")+`
echo "routes=$(ip route | tr '\n' ';')"
echo "dnsoutside=$(nslookup example.com 2>&1 | tr '\n' ' ')"
echo "addresses=$(ip -o -4 addr show | grep -c -v ' lo ')"
`))
	if code != 0 {
		t.Fatalf("probe exit %d: %s", code, out)
	}
	t.Logf("probe:\n%s", out)
	got := results(out)
	if got["loopback"] != "from-helper " {
		t.Errorf("units of one workload do not share loopback: %q", got["loopback"])
	}
	if got["gateway"] != "gateway " {
		t.Errorf("the workload does not reach the gateway: %q", got["gateway"])
	}
	for _, target := range []string{"other", "host", "internet", "metadata"} {
		if !strings.Contains(got[target], "Network is unreachable") {
			t.Errorf("%s: want no route, got %q", target, got[target])
		}
	}
	if !strings.Contains(got["othername"], "bad address") {
		t.Errorf("the name of another workload resolves: %q", got["othername"])
	}
	if strings.Contains(got["routes"], "default") || strings.Count(got["routes"], ";") != 1 {
		t.Errorf("routes = %q, want the workload's own range only", got["routes"])
	}
	if got["addresses"] != "1" {
		t.Errorf("the workload has %s addresses, want 1", got["addresses"])
	}
	if !strings.Contains(got["dnsoutside"], "SERVFAIL") || strings.Contains(got["dnsoutside"], "Name:") {
		t.Errorf("an outside name resolves: %q", got["dnsoutside"])
	}

	// The network itself: internal, and with no address of the host on it.
	netA, _ := NetworkName(a.Ref)
	n, err := c.NetworkInspect(ctx, netA)
	if err != nil {
		t.Fatal(err)
	}
	if !n.isolated() || !testPool.Contains(netip.MustParsePrefix(n.IPAM.Config[0].Subnet).Addr()) {
		t.Errorf("network %+v", n)
	}

	// The gateway reaches workload b's port at its sandbox's name.
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(logs(t, c, gw, LogOptions{}), "from-b") {
		if time.Now().After(deadline) {
			t.Fatalf("the gateway never reached workload b: %q", logs(t, c, gw, LogOptions{}))
		}
		time.Sleep(300 * time.Millisecond)
	}

	// A unit that starts again still shares the sandbox's loopback: the
	// helper is replaced and the next probe finds the new one.
	helperName, _ := ContainerName(a.Ref, "helper")
	if err := c.ContainerRemove(ctx, helperName); err != nil {
		t.Fatal(err)
	}
	start(t, c, a, helper)
	_, out = run(t, c, a, sh("probe", `for i in 1 2 3 4 5 6 7 8 9 10; do wget -q -T 1 -O - http://127.0.0.1:7070/ && break; sleep 0.3; done`))
	if !strings.Contains(out, "from-helper") {
		t.Errorf("after the helper started again: %q", out)
	}

	// Teardown leaves nothing of either workload behind.
	for _, s := range []workload.Spec{a, b} {
		if err := Teardown(ctx, c, s.Ref, gw); err != nil {
			t.Fatal(err)
		}
		left, err := c.ContainerList(ctx, Selector(s.Ref)...)
		if err != nil || len(left) != 0 {
			t.Errorf("containers left: %v, %v", left, err)
		}
		name, _ := NetworkName(s.Ref)
		if _, err := c.NetworkInspect(ctx, name); err == nil {
			t.Errorf("network %s is still there", name)
		}
	}
}
