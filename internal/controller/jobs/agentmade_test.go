package jobs

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// madeInputs is a command job on a template with the platform's files that
// alice's coding agent pi made, and pi itself, working in ~/projects/api
// and let into ~/data to read.
func madeInputs() Inputs {
	in := testInputs(func(j *model.Job) {
		j.Spec.Engine, j.Spec.Kernel, j.Spec.Source = "command", "", model.JobSource{}
		j.Spec.Command = []string{"python", "projects/api/summary.py"}
		j.MadeBy = "pi"
	})
	in.Template.Spec.PodFiles = true
	in.Maker = &model.App{Name: "pi", Owner: in.Job.Owner, Template: "pi", File: "projects/api",
		Folders: []model.FolderGrant{{Path: "data", Mode: model.FolderRead}}}
	return in
}

func runContainers(t *testing.T, in Inputs) (main corev1.Container, all []corev1.Container, volumes []corev1.Volume) {
	t.Helper()
	job, err := BuildManualJob(in, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	return pod.Containers[0], slices.Concat(pod.InitContainers, pod.Containers), pod.Volumes
}

// A run of a job a coding agent made gets no private SSH key, as the agent
// gets none: ~/.ssh is an empty folder of the run wherever the home is.
// Where the platform locks agents in, the whole run starts in that agent's
// lock, with the folder of the job's own runs open to it besides, so the
// command reads and writes nothing the agent could not. The same job made
// by its owner runs as ever.
func TestARunOfAJobAnAgentMadeStaysInsideTheAgentsLimits(t *testing.T) {
	t.Parallel()
	in := madeInputs()
	plain, all, volumes := runContainers(t, in)
	hidden := 0
	for _, c := range all {
		home := slices.ContainsFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/home/user" })
		i := slices.IndexFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/home/user/.ssh" })
		if !home {
			continue
		}
		if i < 0 {
			t.Fatalf("container %s has the home and no mount over ~/.ssh", c.Name)
		}
		if j := slices.IndexFunc(volumes, func(v corev1.Volume) bool { return v.Name == c.VolumeMounts[i].Name }); j < 0 || volumes[j].EmptyDir == nil {
			t.Fatalf("container %s: ~/.ssh is not an empty folder of the run", c.Name)
		}
		hidden++
	}
	if hidden == 0 {
		t.Fatal("no container of the run has the home")
	}
	if slices.ContainsFunc(plain.Command, func(w string) bool { return strings.HasSuffix(w, "agent-lock.sh") }) {
		t.Fatalf("lock off: command = %q", plain.Command)
	}

	in.LockAgents = true
	locked, _, _ := runContainers(t, in)
	want := slices.Concat([]string{"/opt/javo/agents/agent-lock.sh", "/home/user/jobs/nightly",
		"--rw", "/home/user/projects/api", "--ro", "/home/user/data", "--"}, plain.Command)
	if !slices.Equal(locked.Command, want) {
		t.Fatalf("lock on: command =\n%q\nwant\n%q", locked.Command, want)
	}

	in.Job.MadeBy, in.Maker = "", nil
	own, _, _ := runContainers(t, in)
	if !slices.Equal(own.Command, plain.Command) || slices.ContainsFunc(own.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/home/user/.ssh" }) {
		t.Fatalf("alice's own job: command %q, mounts %+v", own.Command, own.VolumeMounts)
	}
}

// A job a coding agent made whose runs cannot start in that agent's lock
// does not run outside it: the agent is gone, the template has none of the
// platform's files, or the job brings an environment of its own.
func TestARunThatCannotBeLockedDoesNotStart(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*Inputs){
		"its agent is gone":         func(in *Inputs) { in.Maker = nil },
		"no platform files":         func(in *Inputs) { in.Template.Spec.PodFiles = false },
		"an environment of its own": func(in *Inputs) { in.Job.Spec.Environment = &model.Environment{Name: "mine", Source: "nebi"} },
	} {
		in := madeInputs()
		in.LockAgents = true
		change(&in)
		if _, err := PlanRun(in, "run-1", nil, testSlot); err == nil || !strings.Contains(err.Error(), "a coding agent made") {
			t.Errorf("%s: PlanRun = %v, want it refused as made by a coding agent", name, err)
		}
	}
}

// The reconciler holds a job a coding agent made to that agent's limits:
// while its runs cannot start in the agent's lock the job is Blocked and
// says why, and once they can, a run starts in it.
func TestTheReconcilerRunsAnAgentsJobOnlyInsideTheAgentsLock(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, func(r *Reconciler) { r.LockAgents = true })
	h.template(func(s *model.TemplateSpec) { s.PodFiles = true })
	made, err := h.db.Jobs().Create(bg, model.Actor{Person: owner, Token: "tok-1", Agent: "pi"}, &model.Job{
		Namespace: h.ns, Name: "summary", Owner: owner,
		Spec: model.JobSpec{Template: h.tplName(), Engine: "command", Command: []string{"python", "projects/api/summary.py"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The agent it names is not there.
	got := h.waitPhase(made, model.JobPhaseBlocked)
	if condReason(got, model.ConditionValid) != string(reasons.AgentLockUnavailable) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}

	if _, err := h.db.Templates().Put(bg, actor, model.Template{Name: "pi", Source: store.SourceAdmin,
		Spec: model.TemplateSpec{DisplayName: "pi", Kind: "pi", Category: model.CategoryAgents, Image: "example.com/pi:1", Port: 8080}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Apps().Create(bg, model.Actor{Person: owner}, &model.App{Namespace: h.ns, Name: "pi", Owner: owner, Template: "pi", File: "projects/api"}); err != nil {
		t.Fatal(err)
	}
	h.waitPhase(made, model.JobPhaseReady)
	_, run := h.requestRun(made, nil)
	c := run.Spec.Template.Spec.Containers[0].Command
	if want := []string{"/opt/javo/agents/agent-lock.sh", "/home/user/jobs/summary", "--rw", "/home/user/projects/api", "--"}; len(c) < len(want) || !slices.Equal(c[:len(want)], want) {
		t.Fatalf("the run's command = %q, want it to start with %q", c, want)
	}
}
