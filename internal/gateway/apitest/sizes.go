package apitest

import (
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

func TestCatalog(t *testing.T) sizes.Catalog {
	t.Helper()
	c, err := sizes.Parse([]byte(`{default: small, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: medium, displayName: Medium, cpu: 500m, memory: 8Gi}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *Fixture) StoredSize(owner, name string) string {
	f.T.Helper()
	return f.GetWorkspace(owner, name).Size
}

// SingleNode is one machine with 8 CPU and about 15.6 GiB allocatable.
func SingleNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("16365140Ki")}},
	}
}

// FitCatalog has the shipped sizes plus one an autoscaler can provide.
func FitCatalog(t *testing.T, def string) sizes.Catalog {
	t.Helper()
	c, err := sizes.Parse([]byte(`{default: ` + def + `, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: medium, displayName: Medium, cpu: "4", memory: 8Gi},
		{name: large, displayName: Large, cpu: "8", memory: 32Gi},
		{name: burst, displayName: Burst, cpu: "16", memory: 64Gi, allowUnschedulable: true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var TooBigOnSingleNode = reasons.Msg(reasons.MsgSizeTooBig, "8", "15.6 GB")

type FitJSON struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	Fits    bool   `json:"fits"`
	Reason  string `json:"reason"`
	Short   string `json:"reasonShort"`
}

func ListFits(t *testing.T, f *Fixture) map[string]FitJSON {
	t.Helper()
	w := f.Do("GET", "/api/sizes", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	out := map[string]FitJSON{}
	for _, s := range Decode[struct{ Items []FitJSON }](t, w).Items {
		out[s.Name] = s
	}
	return out
}

func GpuCatalog(t *testing.T) sizes.Catalog {
	t.Helper()
	c, err := sizes.Parse([]byte(`{default: small, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: gpu, displayName: GPU, cpu: "4", memory: 8Gi, gpu: 1, nodeSelector: {pool: gpu}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
