package apitest

import (
	"net/http"
	"testing"

	"github.com/nebari-dev/javo/internal/sizes"
)

// CustomCatalog: platform admins may ask for any custom amount, group ds up
// to 4 CPU and 16 GB; the gpu size is for group ds.
func CustomCatalog(t *testing.T) sizes.Catalog {
	t.Helper()
	c, err := sizes.Parse([]byte(`{default: small,
		sizes: [
			{name: small, displayName: Small, cpu: "1", memory: 2Gi},
			{name: medium, displayName: Medium, cpu: "4", memory: 8Gi},
			{name: gpu, displayName: GPU, cpu: "2", memory: 4Gi, allowedGroups: [ds]}],
		custom: {enabled: true, rules: [
			{roles: [platform-admin]},
			{groups: [ds], maxCPU: "4", maxMemory: 16Gi}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type AllowanceJSON struct {
	Allowed    bool    `json:"allowed"`
	MaxCPU     float64 `json:"maxCPU"`
	MaxMemory  int64   `json:"maxMemory"`
	MinCPU     float64 `json:"minCPU"`
	MinMemory  int64   `json:"minMemory"`
	CPUStep    float64 `json:"cpuStep"`
	MemoryStep int64   `json:"memoryStep"`
	Limit      string  `json:"limit"`
	Minimum    string  `json:"minimum"`
}

type SizesJSON struct {
	Items  []struct{ Name string }
	Custom AllowanceJSON
}

func (f *Fixture) SizesFor(t *testing.T, id string, groups ...string) SizesJSON {
	t.Helper()
	w := f.Do("GET", "/api/sizes", "", f.Session(id, groups...))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	return Decode[SizesJSON](t, w)
}

func SizeNames(s SizesJSON) []string {
	var out []string
	for _, i := range s.Items {
		out = append(out, i.Name)
	}
	return out
}
