package apitest

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
)

type StubGroups []string

func (g StubGroups) Groups(context.Context, string) ([]string, error) { return g, nil }

type StubUsage struct {
	Calls int
	Got   files.HomeUsage
	// Ns is the namespace the last call's context named.
	Ns string
}

func (s *StubUsage) Usage(ctx context.Context, _ string, enforced bool) (files.HomeUsage, error) {
	s.Calls++
	s.Ns, _ = files.NamespaceOf(ctx)
	if !enforced {
		return files.HomeUsage{}, nil
	}
	return s.Got, nil
}

func Sfact(v bool, p model.Provenance) model.Fact {
	return model.Fact{Value: v, Provenance: p}
}

// ClusterStorage is the singleton as the controller leaves it. A kind cluster
// has one node and local-path storage that does not enforce sizes.
func ClusterStorage(mut ...func(*model.ClusterStorage)) *model.ClusterStorage {
	cs := &model.ClusterStorage{
		Status: model.ClusterStorageStatus{
			Nodes: 1,
			Classes: []model.ClassCapability{{
				Name: "standard", Provisioner: "rancher.io/local-path", IsDefault: true,
				RWXFile: Sfact(false, "known"), PosixOwnership: Sfact(true, "known"), SizeEnforced: Sfact(false, "known"),
				Expansion: Sfact(false, "known"), Snapshots: Sfact(false, "known"),
			}},
			Choice: &model.StorageChoice{Mode: model.HomeModePerUser, SingleNode: true, HomeClass: "standard", Source: "auto"},
		},
	}
	for _, m := range mut {
		m(cs)
	}
	return cs
}

func HomeOf(owner string, mut ...func(*model.Home)) *model.Home {
	h := &model.Home{
		Namespace: gatewaytest.Namespace,
		Spec:      model.HomeSpec{Owner: owner, Size: PtrQ("20Gi")},
		Status:    model.HomeStatus{Mode: model.HomeModePerUser, ClaimName: "home-" + owner},
	}
	for _, m := range mut {
		m(h)
	}
	return h
}

func PtrQ(s string) *resource.Quantity { q := resource.MustParse(s); return &q }

type StorageView struct {
	Mode         string   `json:"mode"`
	ModeLabel    string   `json:"modeLabel"`
	SizeEnforced bool     `json:"sizeEnforced"`
	Size         int64    `json:"size"`
	Used         *int64   `json:"used"`
	ScratchPaths []string `json:"scratchPaths"`
	Notices      []struct {
		Code, Tone, Title, Text, Action string
	} `json:"notices"`
	Folders []struct {
		Group  string `json:"group"`
		Dir    string `json:"dir"`
		Ready  bool   `json:"ready"`
		Reason *struct {
			Code string `json:"code"`
		} `json:"reason"`
	} `json:"folders"`
}

func (v StorageView) Codes() []string {
	var out []string
	for _, n := range v.Notices {
		out = append(out, n.Code)
	}
	return out
}

func GetStorage(t *testing.T, f *Fixture, who *http.Cookie, query string) StorageView {
	t.Helper()
	w := f.Do("GET", "/api/storage"+query, "", who)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/storage: %d %s", w.Code, w.Body)
	}
	return Decode[StorageView](t, w)
}

type FailingUsage struct {
	*StubUsage
	Err error
}

func (f FailingUsage) Usage(context.Context, string, bool) (files.HomeUsage, error) {
	return files.HomeUsage{}, f.Err
}

func Folder(group, name string, ready bool, reason reasons.Code) *model.SharedFolder {
	f := &model.SharedFolder{
		Name: name, Namespace: gatewaytest.Namespace,
		Spec: model.SharedFolderSpec{Group: group},
	}
	if ready {
		f.Status = model.SharedFolderStatus{ClaimName: "c-" + name, GID: 2001, Prepared: true}
		meta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"})
	} else {
		meta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: string(reason)})
	}
	return f
}

type AdminView struct {
	Summary string `json:"summary"`
	Rows    []struct {
		Fact       string `json:"fact"`
		Provenance string `json:"provenance"`
		Attention  bool   `json:"attention"`
		Notice     struct {
			Code string `json:"code"`
		} `json:"notice"`
	} `json:"rows"`
}

func HetznerLike(cs *model.ClusterStorage) {
	cs.Status.Nodes = 3
	cs.Status.Classes = []model.ClassCapability{
		{Name: "hcloud-volumes", IsDefault: true, RWXFile: Sfact(false, "known"), PosixOwnership: Sfact(true, "known"),
			SizeEnforced: Sfact(true, "known"), Expansion: Sfact(true, "known"), Snapshots: Sfact(false, "known")},
		{Name: "longhorn", RWXFile: Sfact(true, "known"), PosixOwnership: Sfact(true, "known"),
			SizeEnforced: Sfact(true, "known"), Expansion: Sfact(true, "known"), Snapshots: Sfact(false, "known"), Notes: []string{"NeedsNfsClient"}},
	}
	cs.Status.Choice = &model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "hcloud-volumes", SharedClass: "longhorn", Source: "auto"}
}

func AdminSession(f *Fixture) *http.Cookie { return f.Session("root-00000000", "javo-admins") }

func Node(name, cpu, mem string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)},
	}}
}

func AppPod(name, Node, owner, ws, cpu string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gatewaytest.Namespace, Labels: map[string]string{model.LabelOwner: owner, model.LabelApp: ws}},
		Spec: corev1.PodSpec{NodeName: Node, Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse("1Gi")},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TemplateWith(name, cpu string) *model.Template {
	t := gatewaytest.Template(name, "jupyterlab")
	t.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse("1Gi")}
	return t
}

type PlacementView struct {
	CoLocated   bool     `json:"coLocated"`
	Node        string   `json:"node"`
	FreeCPU     float64  `json:"freeCPU"`
	FreeMemory  int64    `json:"freeMemory"`
	Fits        bool     `json:"fits"`
	RunningApps []string `json:"runningApps"`
	Notice      *struct {
		Code, Tone, Title, Text, Action string
	} `json:"notice"`
}

// alice has two apps on node-a, which has 8 CPUs; the two apps use 3 together
// with a pod of someone else, so 5 are free.
func PlacementObjs(mode model.HomeMode, nodes int32) []any {
	cs := ClusterStorage(func(cs *model.ClusterStorage) {
		cs.Status.Nodes, cs.Status.Choice.SingleNode, cs.Status.Choice.Mode = nodes, nodes == 1, mode
	})
	sales, forecast := naming.ObjectName(AliceID, "sales-app"), naming.ObjectName(AliceID, "forecast")
	w1, w2 := gatewaytest.Workspace("sales-app", AliceID, "Sales"), gatewaytest.Workspace("forecast", AliceID, "Forecast")
	return []any{
		cs, HomeOf(AliceID, func(h *model.Home) { h.Status.Mode = mode }), Node("node-a", "8", "32Gi"), Node("node-b", "8", "32Gi"),
		AppPod("p1", "node-a", AliceID, sales, "1"), AppPod("p2", "node-a", AliceID, forecast, "1"),
		AppPod("p3", "node-a", BobID, "bob-app", "1"), w1, w2,
		TemplateWith("small", "1"), TemplateWith("huge", "6"),
	}
}

func GetPlacement(t *testing.T, f *Fixture, query string) (int, PlacementView) {
	t.Helper()
	w := f.Do("GET", "/api/storage/placement"+query, "", f.Session(AliceID))
	if w.Code != http.StatusOK {
		t.Fatalf("placement%s: %d %s", query, w.Code, w.Body)
	}
	return w.Code, Decode[PlacementView](t, w)
}

// StoredRequests is the admin's requests as the database holds them.
func StoredRequests(t *testing.T, f *Fixture) model.StorageRequests {
	t.Helper()
	r, err := f.Db.Storage().Requests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// PlacementFixture is New for objects with app pods: the pods name
// their app by its object name, and here get the label value the controller
// gives them, the app's row id, which only exists once the apps are stored.
func PlacementFixture(t *testing.T, o Options, objs ...any) *Fixture {
	t.Helper()
	f := New(t, o, objs...)
	ids := map[string]string{}
	for _, a := range f.Apps() {
		ids[a.ObjectName()] = a.ID
	}
	var pods corev1.PodList
	if err := f.Client.List(context.Background(), &pods); err != nil {
		t.Fatal(err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if id, ok := ids[p.Labels[model.LabelApp]]; ok {
			p.Labels[model.LabelApp] = id
			if err := f.Client.Update(context.Background(), p); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}
