package storage

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/nebari-dev/javo/internal/model"
)

const quotaName = "javo-storage"

func q(s string) *resource.Quantity { v := resource.MustParse(s); return &v }

func teamNamespace(t *testing.T, name string) {
	t.Helper()
	must(t, k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
}

func runQuota(t *testing.T, teams ...TeamNamespace) {
	t.Helper()
	r := &QuotaReconciler{Client: k8s, Rows: testRows(), Teams: teams}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: SingletonName}}); err != nil {
		t.Fatal(err)
	}
}

func getQuota(t *testing.T, ns string) *corev1.ResourceQuota {
	t.Helper()
	rq := &corev1.ResourceQuota{}
	err := k8s.Get(ctx, types.NamespacedName{Namespace: ns, Name: quotaName}, rq)
	if apierrors.IsNotFound(err) {
		return nil
	}
	must(t, err)
	return rq
}

func hard(rq *corev1.ResourceQuota, key string) string {
	v := rq.Spec.Hard[corev1.ResourceName(key)]
	return v.String()
}

func TestAPerTeamNamespaceGetsAStorageQuotaForTheTotalAndTheHomeClass(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	teamNamespace(t, "javo-quota-a")
	runQuota(t, TeamNamespace{Namespace: "javo-quota-a", Limit: q("100Gi")})

	rq := getQuota(t, "javo-quota-a")
	if rq == nil {
		t.Fatal("no quota made")
	}
	if got := hard(rq, "requests.storage"); got != "100Gi" {
		t.Errorf("requests.storage = %s", got)
	}
	if got := hard(rq, "fast.storageclass.storage.k8s.io/requests.storage"); got != "100Gi" {
		t.Errorf("class-scoped requests.storage = %s", got)
	}
	if len(rq.Spec.Hard) != 2 {
		t.Errorf("hard = %v, want exactly the two storage keys", rq.Spec.Hard)
	}
}

func TestAChangedTeamLimitUpdatesTheQuota(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	teamNamespace(t, "javo-quota-b")
	runQuota(t, TeamNamespace{Namespace: "javo-quota-b", Limit: q("100Gi")})
	runQuota(t, TeamNamespace{Namespace: "javo-quota-b", Limit: q("250Gi")})

	rq := getQuota(t, "javo-quota-b")
	if hard(rq, "requests.storage") != "250Gi" || hard(rq, "fast.storageclass.storage.k8s.io/requests.storage") != "250Gi" {
		t.Errorf("hard = %v, want 250Gi on both keys", rq.Spec.Hard)
	}
}

func TestATeamWithoutItsOwnLimitGetsTheClusterDefault(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	cfg.TeamStorage.Limit = q("50Gi")
	teamNamespace(t, "javo-quota-c")
	teamNamespace(t, "javo-quota-d")
	runQuota(t, TeamNamespace{Namespace: "javo-quota-c"}, TeamNamespace{Namespace: "javo-quota-d", Limit: q("10Gi")})

	if got := hard(getQuota(t, "javo-quota-c"), "requests.storage"); got != "50Gi" {
		t.Errorf("default limit = %s, want 50Gi", got)
	}
	if got := hard(getQuota(t, "javo-quota-d"), "requests.storage"); got != "10Gi" {
		t.Errorf("team limit = %s, want 10Gi", got)
	}
}

func TestATeamWithNoLimitAnywhereGetsNoQuotaAndLosesOurOldOne(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	teamNamespace(t, "javo-quota-e")
	runQuota(t, TeamNamespace{Namespace: "javo-quota-e", Limit: q("10Gi")})
	runQuota(t, TeamNamespace{Namespace: "javo-quota-e"})
	if getQuota(t, "javo-quota-e") != nil {
		t.Fatal("the quota stays after the limit was removed")
	}
}

func TestAQuotaOfTheSameNameThatIsNotOursIsLeftAlone(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	teamNamespace(t, "javo-quota-f")
	foreign := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: quotaName, Namespace: "javo-quota-f"},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("5")}}}
	must(t, k8s.Create(ctx, foreign))

	r := &QuotaReconciler{Client: k8s, Rows: testRows(), Teams: []TeamNamespace{{Namespace: "javo-quota-f", Limit: q("10Gi")}}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: SingletonName}}); err == nil {
		t.Error("a quota we did not make must be reported, not silently used")
	}
	rq := getQuota(t, "javo-quota-f")
	if _, ok := rq.Spec.Hard[corev1.ResourcePods]; !ok || len(rq.Spec.Hard) != 1 {
		t.Errorf("foreign quota changed: %v", rq.Spec.Hard)
	}
	r.Teams[0].Limit = nil
	_, _ = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: SingletonName}})
	if getQuota(t, "javo-quota-f") == nil {
		t.Error("foreign quota deleted")
	}
}

func TestAClassScopedKeyIsLeftOutUntilAClassIsChosen(t *testing.T) {
	clean(t)

	teamNamespace(t, "javo-quota-g")
	runQuota(t, TeamNamespace{Namespace: "javo-quota-g", Limit: q("10Gi")})
	rq := getQuota(t, "javo-quota-g")
	if len(rq.Spec.Hard) != 1 || hard(rq, "requests.storage") != "10Gi" {
		t.Errorf("hard = %v, want only the total", rq.Spec.Hard)
	}
}
