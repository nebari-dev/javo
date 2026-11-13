// Package storage holds the reconcilers that keep the platform's storage
// state: what the cluster offers and which home mode is in effect.
package storage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop/managed"
	"github.com/nebari-dev/javo/internal/controller/storage/rows"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/capability"
	"github.com/nebari-dev/javo/internal/storage/choose"
	"github.com/nebari-dev/javo/internal/storage/inventory"
	"github.com/nebari-dev/javo/internal/storage/migrate"
	"github.com/nebari-dev/javo/internal/store"
)

// Reasons for conditions that are fine. Failures come from internal/reasons.
const (
	reasonReady         = rows.ReasonReady
	reasonNotApplicable = "NotApplicable"
)

// ClusterReconciler keeps the cluster's storage findings: what the cluster
// offers (classes with their capabilities, node and zone counts), the mode in
// effect, the move plan and the conditions. Everything it writes is derived,
// so it can be rebuilt from the cluster at any time.
type ClusterReconciler struct {
	client.Client
	Rows
	// Reader reads VolumeSnapshotClass, whose CRD may be absent and so cannot
	// be cached. Everything else is read through Client. Nil means Client.
	Reader client.Reader
}

// RBAC: see internal/controller/workspace/reconciler.go for how the chart's
// Roles are checked against these markers.
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses;csidrivers;csinodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotclasses,verbs=get;list;watch

// Reconcile refreshes the findings whatever triggered it.
func (r *ClusterReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	cs, err := r.Cluster(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	var reader client.Reader = r.Client
	if r.Reader != nil {
		reader = r.Reader
	}
	inv, err := inventory.Read(ctx, r.Client, reader)
	if err != nil {
		return ctrl.Result{}, err
	}
	homes, err := r.Store.Homes().List(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list homes: %w", err)
	}

	want := derive(cs, inv, homes)
	if equality.Semantic.DeepEqual(cs.Status, want) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Store.Storage().SetClusterStatus(ctx, want)
}

// homesMadeOnOneNode counts the perUser homes that were created while the
// cluster had one node. The fact is on the home, so it survives the findings
// being rebuilt.
func homesMadeOnOneNode(homes []model.Home) int {
	return len(slices.DeleteFunc(slices.Clone(homes), func(h model.Home) bool {
		return h.Status.Mode != model.HomeModePerUser || !h.Status.CreatedOnSingleNode
	}))
}

// failure sets a condition to False with the catalog's reason and text. The
// evidence says what was found, so the text is not a bare sentence.
func failure(code reasons.Code, evidence string) (string, string) {
	r, _ := reasons.Lookup(code)
	return string(code), fmt.Sprintf("%s (%s)", r.What, evidence)
}

// finishedProbe is the last finished probe of a class, or nil.
func finishedProbe(results []model.ProbeStatus, class string) *model.ProbeStatus {
	if i := slices.IndexFunc(results, func(p model.ProbeStatus) bool { return p.StorageClassName == class }); i >= 0 {
		return &results[i]
	}
	return nil
}

// derive builds the status ClusterStorage should have. Conditions keep their
// transition time when their status does not change.
func derive(cs *model.ClusterStorage, inv inventory.Inventory, homes []model.Home) model.ClusterStorageStatus {
	prev := cs.Status
	caps := make([]model.ClassCapability, 0, len(inv.Classes))
	for i := range inv.Classes {
		sc := &inv.Classes[i]
		var drv *storagev1.CSIDriver
		if d, ok := inv.Drivers[sc.Provisioner]; ok {
			drv = &d
		}
		caps = append(caps, capability.Classify(sc, drv, inv.CSINodes, cs.Spec.ClassOverrides, finishedProbe(prev.ProbeResults, sc.Name), inv.SnapshotDrivers))
	}
	slices.SortFunc(caps, func(a, b model.ClassCapability) int { return cmp.Compare(a.Name, b.Name) })

	choice := choose.Choose(caps, inv.Nodes, cs.Spec)
	out := model.ClusterStorageStatus{
		Nodes: int32(inv.Nodes), Zones: inv.Zones, Classes: caps, Choice: &choice,
		Probe: prev.Probe, ProbeResults: prev.ProbeResults,
		Conditions: slices.Clone(prev.Conditions),
	}
	// The plan is rebuilt from the Homes every pass; it changes nothing.
	if plan := migrate.Plan(homes, choice); plan.Users > 0 {
		out.Migration = &plan
	}
	set := func(typ string, status metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(&out.Conditions, metav1.Condition{
			Type: typ, Status: status, Reason: reason, Message: msg,
		})
	}

	home := func(code reasons.Code, evidence string) {
		reason, msg := failure(code, evidence)
		set(model.ConditionHomeStorageReady, metav1.ConditionFalse, reason, msg)
	}
	switch {
	case len(caps) == 0:
		home(reasons.NoStorageClass, "StorageClasses found: 0")
	case choice.HomeClass != "" && !slices.ContainsFunc(caps, func(c model.ClassCapability) bool { return c.Name == choice.HomeClass }):
		home(reasons.NamedStorageClassMissing, fmt.Sprintf("named class %q is not among the %d StorageClasses found", choice.HomeClass, len(caps)))
	case choice.Mode == model.HomeModeShared && !homeClassCanShare(caps, choice.HomeClass):
		evidence := fmt.Sprintf("%d StorageClasses checked, none with shared files and POSIX ownership", len(caps))
		if choice.HomeClass != "" {
			evidence = fmt.Sprintf("StorageClass %q lacks shared files or POSIX ownership", choice.HomeClass)
		}
		home(reasons.OverrideNotSupported, evidence)
	case choice.HomeClass == "":
		home(reasons.NoDefaultStorageClass, fmt.Sprintf("%d StorageClasses found, none default", len(caps)))
	default:
		set(model.ConditionHomeStorageReady, metav1.ConditionTrue, reasonReady,
			fmt.Sprintf("Homes use the %s mode on StorageClass %s.", choice.Mode, choice.HomeClass))
	}

	// perUser homes made on a one-node cluster have no co-location to rely on
	// once another node joins. The Homes carry the fact, not this status.
	oneNodeHomes := homesMadeOnOneNode(homes)
	if inv.Nodes > 1 && oneNodeHomes > 0 {
		reason, msg := failure(reasons.SecondNodeJoined, fmt.Sprintf("%d perUser homes made on one node, %d nodes now", oneNodeHomes, inv.Nodes))
		set(model.ConditionSingleNodeAssumption, metav1.ConditionTrue, reason, msg)
	} else {
		set(model.ConditionSingleNodeAssumption, metav1.ConditionFalse, reasonNotApplicable, "")
	}

	if m := out.Migration; m != nil && m.Users > m.Done {
		reason, msg := failure(reasons.HomeModeMismatch, fmt.Sprintf("%d homes on %s, the cluster uses %s", m.Users-m.Done, m.From, m.To))
		set(model.ConditionHomeModeMismatch, metav1.ConditionTrue, reason, msg)
	} else {
		set(model.ConditionHomeModeMismatch, metav1.ConditionFalse, reasonNotApplicable, "")
	}
	return out
}

func homeClassCanShare(caps []model.ClassCapability, name string) bool {
	i := slices.IndexFunc(caps, func(c model.ClassCapability) bool { return c.Name == name })
	return i >= 0 && choose.CanShare(caps[i])
}

// nodeChanges lets only the node changes that alter the inventory through: a
// node joining or leaving, or its labels (zone) changing. Status updates,
// which every node sends all the time, are dropped.
var nodeChanges = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		return !equality.Semantic.DeepEqual(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels())
	},
	GenericFunc: func(event.GenericEvent) bool { return false },
}

// SetupWithManager reconciles the findings on any change to what they
// report: the cluster's storage objects and nodes, the homes, and the probe
// results (the probe controller's part of the findings).
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil {
		return errors.New("storage.ClusterReconciler: Store is required")
	}
	all := func(context.Context) ([]string, error) { return singletonID, nil }
	toSingletonRow := func(context.Context, string) ([]string, error) { return singletonID, nil }
	return ctrl.NewControllerManagedBy(mgr).Named("cluster-storage").
		WatchesRawSource(managed.Source(dbsource.For(feed.Subscribe(), store.TableHomes, 0, all))).
		WatchesRawSource(managed.Source(dbsource.Fan(feed.Subscribe(), store.TableStorageStatus, toSingletonRow))).
		Watches(&storagev1.StorageClass{}, toSingleton).
		Watches(&storagev1.CSIDriver{}, toSingleton).
		Watches(&storagev1.CSINode{}, toSingleton).
		Watches(&corev1.Node{}, toSingleton, builder.WithPredicates(nodeChanges)).
		Complete(r)
}
