package docker

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/workload"
)

// ReadNets gives every address range of the daemon's networks and the
// pool, and says which workload a network of this client's is.
func TestReadNetsMapsRangesToWorkloads(t *testing.T) {
	t.Parallel()
	subnet := func(cidr string) map[string]any {
		return map[string]any{"Config": []map[string]string{{"Subnet": cidr}}}
	}
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/networks") {
			fail(w, http.StatusNotFound, "no such thing")
			return
		}
		reply(w, http.StatusOK, []map[string]any{
			{"Name": "bridge", "IPAM": subnet("172.17.0.0/16")},
			{"Name": "compose_default", "IPAM": subnet("192.0.2.0/24")},
			{"Name": "host"},
			{"Name": "javo-alice.notebook", "IPAM": subnet("10.213.0.16/28"), "Labels": Labels(ref)},
			// Another Javo's, and a network that only claims a name.
			{"Name": "javo-bob.other", "IPAM": subnet("10.213.0.32/28"), "Labels": map[string]string{LabelManaged: "true", LabelNamespace: "bob", LabelName: "other", "javo.instance": "two"}},
			{"Name": "javo-eve.fake", "IPAM": subnet("198.51.100.0/24"), "Labels": map[string]string{LabelNamespace: "eve", LabelName: "fake"}},
		})
	})
	c.Labels = map[string]string{"javo.instance": "one"}

	nets, err := ReadNets(t.Context(), c, netip.Prefix{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.213.0.0/16", "10.213.0.16/28", "10.213.0.32/28", "172.17.0.0/16", "192.0.2.0/24", "198.51.100.0/24"}
	var got []string
	for _, p := range nets.Ranges {
		got = append(got, p.String())
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("ranges %v, want %v", got, want)
	}
	// The fake daemon above does not filter by label, so the notebook's
	// network, which lacks this client's label, must be left out too.
	if len(nets.Workloads) != 0 {
		t.Errorf("workloads %v, want none: none carries this client's labels", nets.Workloads)
	}

	c.Labels = nil
	nets, err = ReadNets(t.Context(), c, netip.MustParsePrefix("10.99.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	wantApps := map[netip.Prefix]workload.Ref{
		netip.MustParsePrefix("10.213.0.16/28"): ref,
		netip.MustParsePrefix("10.213.0.32/28"): {Namespace: "bob", Name: "other"},
	}
	if len(nets.Workloads) != 2 || nets.Workloads[netip.MustParsePrefix("10.213.0.16/28")] != ref || nets.Workloads[netip.MustParsePrefix("10.213.0.32/28")] != wantApps[netip.MustParsePrefix("10.213.0.32/28")] {
		t.Errorf("workloads %v, want %v", nets.Workloads, wantApps)
	}
	if !slices.Contains(nets.Ranges, netip.MustParsePrefix("10.99.0.0/16")) {
		t.Errorf("the pool is not among the ranges: %v", nets.Ranges)
	}
}
