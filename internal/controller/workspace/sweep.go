package workspace

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Sweep removes what carries an app id no row has any more: what an app
// left behind when its row was removed without the controller running. It
// asks the executor first and reads the rows after, so a workload started
// while the sweep runs belongs to a row that is among them.
func (r *Reconciler) Sweep(ctx context.Context) error {
	refs, err := r.Exec.List(ctx)
	if err != nil {
		return err
	}
	apps, err := r.Store.Apps().ListAll(ctx)
	if err != nil {
		return err
	}
	live := make(map[string]bool, len(apps))
	for _, a := range apps {
		live[a.ID] = true
	}
	var failed error
	for _, ref := range refs {
		if live[ref.ID] {
			continue
		}
		log.FromContext(ctx).Info("removing what an app whose row is gone left", "namespace", ref.Namespace, "name", ref.Name)
		if err := r.remove(ctx, ref); err != nil {
			failed = err
		}
	}
	return failed
}
