// Package notify tells a job's owner when a run fails. A leader-only runnable
// looks, every Interval, for finished failed runs in the database whose
// owner has not been told. For each it evaluates the operator's ConfigMap
// trigger and sends the notice through the notifications-engine API, then
// records the time in job_runs.notified_at, so a restart never sends twice. A
// send error is shown on the job as the condition Notified=False and is not
// retried for that run.
package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/argoproj/notifications-engine/pkg/api"
	"github.com/argoproj/notifications-engine/pkg/services"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// TriggerRunFailed is the trigger name the operator's ConfigMap defines
// (trigger.on-run-failed); spec.notify.onFailure=false removes its recipients.
const TriggerRunFailed = "on-run-failed"

// emailService is the service name under which the owner's own address is
// added as a recipient. It is skipped when the ConfigMap defines no such service.
const emailService = "email"

const defaultInterval = 15 * time.Second

// RecipientResolver finds the verified email of a job's owner. Owners without
// one (for example local users) are not mailed and see the failure in the app.
type RecipientResolver interface {
	EmailFor(ctx context.Context, ownerName string) (string, bool)
}

// NoRecipients resolves no owner, so only the ConfigMap's own subscriptions send.
type NoRecipients struct{}

func (NoRecipients) EmailFor(context.Context, string) (string, bool) { return "", false }

// Config wires the notifier. Namespace holds the ConfigMap and Secret, which
// Kube reads; Store holds the runs.
type Config struct {
	Store                     *store.Store
	Kube                      kubernetes.Interface
	Namespace                 string
	ConfigMapName, SecretName string
	Recipients                RecipientResolver
	// Interval is how often finished runs are looked at; zero means 15 seconds.
	Interval time.Duration
}

// The notifier reads the operator's ConfigMap and Secret in the release
// namespace; the chart limits those two to the configured names.
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch,resourceNames=javo-notifications
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch,resourceNames=javo-notifications

// Notifier sends the failure notices. It only runs on the leader, so a
// restart or second replica never sends twice.
type Notifier struct{ Config }

// New returns the notifier.
func New(cfg Config) (*Notifier, error) {
	switch {
	case cfg.Store == nil || cfg.Kube == nil || cfg.Recipients == nil:
		return nil, errors.New("notify: Store, Kube and Recipients are required")
	case cfg.Namespace == "" || cfg.ConfigMapName == "" || cfg.SecretName == "":
		return nil, errors.New("notify: Namespace, ConfigMapName and SecretName are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	return &Notifier{cfg}, nil
}

// NeedLeaderElection makes the manager start the notifier on the leader only.
func (n *Notifier) NeedLeaderElection() bool { return true }

// Start runs until ctx ends.
func (n *Notifier) Start(ctx context.Context) error {
	cm := n.namedInformer(n.ConfigMapName, func(f kubeinformers.SharedInformerFactory) cache.SharedIndexInformer {
		return f.Core().V1().ConfigMaps().Informer()
	})
	secret := n.namedInformer(n.SecretName, func(f kubeinformers.SharedInformerFactory) cache.SharedIndexInformer {
		return f.Core().V1().Secrets().Informer()
	})
	factory := api.NewFactory(api.Settings{
		ConfigMapName: n.ConfigMapName, SecretName: n.SecretName,
		DefaultNamespace: n.Namespace, InitGetVars: initGetVars,
	}, n.Namespace, secret, cm)
	for _, inf := range []cache.SharedIndexInformer{cm, secret} {
		go inf.Run(ctx.Done())
	}
	if !cache.WaitForCacheSync(ctx.Done(), cm.HasSynced, secret.HasSynced) {
		return nil
	}
	tick := time.NewTicker(n.Interval)
	defer tick.Stop()
	for {
		if err := n.Sweep(ctx, factory); err != nil && ctx.Err() == nil {
			logf.Log.WithName("notify").Error(err, "sending failure notices")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// namedInformer watches only the one object called name, so the controller's
// permission can be limited to that name.
func (n *Notifier) namedInformer(name string, pick func(kubeinformers.SharedInformerFactory) cache.SharedIndexInformer) cache.SharedIndexInformer {
	f := kubeinformers.NewSharedInformerFactoryWithOptions(n.Kube, 10*time.Minute, kubeinformers.WithNamespace(n.Namespace),
		kubeinformers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
		}))
	return pick(f)
}

func initGetVars(*api.Config, *corev1.ConfigMap, *corev1.Secret) (api.GetVars, error) {
	return TemplateVars, nil
}

// Sweep tells the owner of each finished failed run that has not been told,
// then marks it. A run is marked whether or not anything was sent (onFailure
// off, no recipient, send error), so it is looked at once.
func (n *Notifier) Sweep(ctx context.Context, factory api.Factory) error {
	runs, err := n.Store.Jobs().UnnotifiedFailedRuns(ctx)
	if err != nil || len(runs) == 0 {
		return err
	}
	a, err := factory.GetAPI()
	if err != nil {
		return err
	}
	for _, run := range runs {
		job, err := n.Store.Jobs().Get(ctx, run.JobID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		sent, sendErr := n.notify(ctx, a, &job, run)
		if err := n.Store.Jobs().MarkRunNotified(ctx, run.ID, time.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var cond *metav1.Condition
		switch {
		case sendErr != nil:
			logf.Log.WithName("notify").Error(sendErr, "sending a failure notice", "job", job.Name, "namespace", job.Namespace, "run", run.ID)
			c := notifiedCondition(metav1.ConditionFalse, reasons.NotificationFailed)
			cond = &c
		case sent:
			c := notifiedCondition(metav1.ConditionTrue, reasons.Ready)
			cond = &c
		}
		if cond != nil {
			if err := n.setCondition(ctx, job.ID, *cond); err != nil {
				// The send error is already logged; this is a second failure.
				logf.Log.WithName("notify").Error(err, "recording Notified condition", "job", job.Name, "namespace", job.Namespace)
			}
		}
	}
	return nil
}

// notify runs the triggers of the run's destinations and sends what they
// fire. It returns the first send error, and whether anything was delivered.
func (n *Notifier) notify(ctx context.Context, a api.API, job *model.Job, run model.Run) (sent bool, sendErr error) {
	obj := objectOf(job, run)
	for trigger, dests := range n.destinations(ctx, job, a.GetConfig()) {
		results, err := a.RunTrigger(trigger, obj)
		if err != nil {
			sendErr = errors.Join(sendErr, fmt.Errorf("trigger %s: %w", trigger, err))
		}
		for _, r := range results {
			if !r.Triggered {
				continue
			}
			for _, to := range dests {
				if err := a.Send(obj, r.Templates, to); err != nil {
					sendErr = errors.Join(sendErr, fmt.Errorf("deliver %s to %s: %w", trigger, to, err))
				} else {
					sent = true
				}
			}
		}
	}
	return sent, sendErr
}

// objectOf is what triggers and templates read as `obj`: the job as a
// Kubernetes object would show it, with the run as its newest (only) entry in
// status.recentRuns, which is where the operator's example trigger looks.
func objectOf(job *model.Job, run model.Run) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": job.Name, "namespace": job.Namespace},
		"spec":     map[string]any{"displayName": job.Spec.DisplayName, "owner": job.Owner},
		"status": map[string]any{"recentRuns": []any{map[string]any{
			"id": run.ID, "trigger": run.Trigger, "outcome": run.Outcome, "reason": run.Reason, "message": run.Message,
		}}},
	}
}

// TemplateVars gives triggers and templates `obj` (the job as objectOf builds it)
// and three shortcuts: `job`, the newest `run`, and `reason`, the catalog entry
// for that run's reason code, so notices use the same words as the app.
func TemplateVars(obj map[string]any, _ services.Destination) map[string]any {
	u := unstructured.Unstructured{Object: obj}
	name := u.GetName()
	if dn, _, _ := unstructured.NestedString(obj, "spec", "displayName"); dn != "" {
		name = dn
	}
	run := newestRun(obj)
	reason := map[string]any{"title": "", "what": "", "todo": ""}
	if code, _ := run["reason"].(string); code != "" {
		if r, ok := reasons.Lookup(reasons.Code(code)); ok {
			reason = map[string]any{"title": r.Title, "what": r.What, "todo": r.Todo}
		}
	}
	return map[string]any{
		"obj": obj, "run": run, "reason": reason,
		"job": map[string]any{"name": u.GetName(), "namespace": u.GetNamespace(), "displayName": name},
	}
}

func newestRun(obj map[string]any) map[string]any {
	runs, _, _ := unstructured.NestedSlice(obj, "status", "recentRuns")
	if len(runs) > 0 {
		if m, ok := runs[0].(map[string]any); ok {
			return m
		}
	}
	return map[string]any{}
}

// destinations applies the job's notify setting to what the ConfigMap's
// subscriptions produced: onFailure=false removes the failure recipients,
// otherwise the owner's verified email is added. Nothing a user writes names
// a recipient.
func (n *Notifier) destinations(ctx context.Context, job *model.Job, cfg api.Config) services.Destinations {
	dests := cfg.GetGlobalDestinations(nil)
	if on := job.Spec.Notify.OnFailure; on != nil && !*on {
		delete(dests, TriggerRunFailed)
		return dests.Dedup()
	}
	if _, configured := cfg.Services[emailService]; configured {
		if addr, ok := n.Recipients.EmailFor(ctx, job.Owner); ok {
			dests[TriggerRunFailed] = append(dests[TriggerRunFailed], services.Destination{Service: emailService, Recipient: addr})
		}
	}
	return dests.Dedup()
}

func notifiedCondition(status metav1.ConditionStatus, code reasons.Code) metav1.Condition {
	r, _ := reasons.Lookup(code)
	return metav1.Condition{Type: model.ConditionNotified, Status: status, Reason: string(code), Message: r.Title}
}

// setCondition writes cond on the job's status if it differs. The store
// merges it into the stored status in one transaction, and keeps it when the
// reconciler writes status.
func (n *Notifier) setCondition(ctx context.Context, jobID string, cond metav1.Condition) error {
	job, err := n.Store.Jobs().Get(ctx, jobID)
	if err != nil {
		return err
	}
	cond.ObservedGeneration = job.Version
	before := meta.FindStatusCondition(job.Status.Conditions, cond.Type)
	if before != nil && before.Status == cond.Status && before.Reason == cond.Reason && before.ObservedGeneration == cond.ObservedGeneration {
		return nil
	}
	return n.Store.Jobs().SetCondition(ctx, job.ID, cond)
}
