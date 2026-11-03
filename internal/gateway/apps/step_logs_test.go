package apps_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
)

// The environment step streams the tools' log, then pixi's install output
// line by line as it is written, waiting for the install to start, and ends
// once the install has finished.
func TestStepStreamFollowsEachContainerInTurn(t *testing.T) {
	apitest.FastPoll(t)
	pod := gatewaytest.PodWith("app", "env-tools", "env-install")
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{apitest.RunningCS("env-tools", 0)}
	logs := apitest.NewScriptLogs()
	tools := logs.Follow(apitest.LogKey(pod.Name, "env-tools", false))
	f := apitest.New(t, apitest.Options{Logs: logs}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"), gatewaytest.Template("streamlit", "streamlit"), pod)
	recs, _ := apitest.StepStream(t, f, "app", model.ConditionEnvReady)

	apitest.Expect(t, recs, "start env-tools")
	io.WriteString(tools, "2026-10-30T12:00:01Z copied javo\n")
	apitest.Expect(t, recs, "line env-tools copied javo")

	// The tools finish; the install has not started yet.
	apitest.SetStatuses(t, f, pod, apitest.Terminated("env-tools", 0, "Completed", ""))
	install := logs.Follow(apitest.LogKey(pod.Name, "env-install", false))
	tools.Close()
	apitest.Expect(t, recs, "exit env-tools exit=0 (Completed)")

	apitest.SetStatuses(t, f, pod, apitest.Terminated("env-tools", 0, "Completed", ""), apitest.RunningCS("env-install", 0))
	apitest.Expect(t, recs, "start env-install")
	io.WriteString(install, "2026-10-30T12:00:02Z Installing the template's environment\n")
	apitest.Expect(t, recs, "line env-install Installing the template's environment")
	io.WriteString(install, "2026-10-30T12:00:03Z ✔ The default environment has been installed.\n")
	apitest.Expect(t, recs, "line env-install ✔ The default environment has been installed.")

	apitest.SetStatuses(t, f, pod, apitest.Terminated("env-tools", 0, "Completed", ""), apitest.Terminated("env-install", 0, "Completed", ""))
	install.Close()
	apitest.Expect(t, recs, "exit env-install exit=0 (Completed)")
	apitest.ExpectEnd(t, recs)
}

// A finished step answers its whole output at once and ends; a failed
// container's termination message comes after its log.
func TestStepStreamOfAFailedInstall(t *testing.T) {
	apitest.FastPoll(t)
	pod := gatewaytest.PodWith("app", "env-tools", "env-install")
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		apitest.Terminated("env-tools", 0, "Completed", ""),
		apitest.Terminated("env-install", 1, "Error", "Error: the platform linux-64 is not part of the lock file"),
	}
	pod.Status.Phase = corev1.PodFailed
	logs := apitest.NewScriptLogs()
	logs.Bodies[apitest.LogKey(pod.Name, "env-install", false)] = "2026-10-30T12:00:02Z Installing\n2026-10-30T12:00:03Z Error: the platform linux-64 is not part of the lock file\n"
	f := apitest.New(t, apitest.Options{Logs: logs}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"), gatewaytest.Template("streamlit", "streamlit"), pod)
	recs, _ := apitest.StepStream(t, f, "app", model.ConditionEnvReady)
	apitest.Expect(t, recs,
		"start env-tools", "exit env-tools exit=0 (Completed)",
		"start env-install", "line env-install Installing", "line env-install Error: the platform linux-64 is not part of the lock file",
		"exit env-install Error: the platform linux-64 is not part of the lock file exit=1 (Error)")
	apitest.ExpectEnd(t, recs)
}

// An app that crashed and restarted shows the run that crashed, how it
// ended and the restart, then follows the new run; a crash while watching
// shows the same, waits out the back-off and follows the next run.
func TestStepStreamShowsCrashesAndRestarts(t *testing.T) {
	apitest.FastPoll(t)
	pod := gatewaytest.PodWith("app")
	cs := apitest.RunningCS(podspec.ContainerName, 1)
	cs.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
	logs := apitest.NewScriptLogs()
	logs.Bodies[apitest.LogKey(pod.Name, podspec.ContainerName, true)] = "2026-10-30T12:00:01Z ModuleNotFoundError: No module named 'plotly'\n"
	run := logs.Follow(apitest.LogKey(pod.Name, podspec.ContainerName, false))
	f := apitest.New(t, apitest.Options{Logs: logs}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"), gatewaytest.Template("streamlit", "streamlit"), pod)
	recs, _ := apitest.StepStream(t, f, "app", model.ConditionStarted)

	apitest.Expect(t, recs, "start app", "line app ModuleNotFoundError: No module named 'plotly'", "exit app exit=1 (Error)", "restart app restarts=1")
	io.WriteString(run, "2026-10-30T12:00:05Z Collecting usage statistics.\n")
	apitest.Expect(t, recs, "line app Collecting usage statistics.")

	// It crashes again and waits to be restarted.
	crashed := corev1.ContainerStatus{Name: podspec.ContainerName, RestartCount: 1,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 20s restarting failed container"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error", Message: "boom"}}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{crashed}
	apitest.SetStatuses(t, f, pod)
	next2 := logs.Follow(apitest.LogKey(pod.Name, podspec.ContainerName, false))
	run.Close()
	apitest.Expect(t, recs, "exit app boom exit=2 (Error)", "waiting app back-off 20s restarting failed container (CrashLoopBackOff)")

	pod.Status.ContainerStatuses = []corev1.ContainerStatus{apitest.RunningCS(podspec.ContainerName, 2)}
	apitest.SetStatuses(t, f, pod)
	apitest.Expect(t, recs, "restart app restarts=2")
	io.WriteString(next2, "2026-10-30T12:00:30Z You can now view your Streamlit app\n")
	apitest.Expect(t, recs, "line app You can now view your Streamlit app")
}

// The home step streams the log of the Job pod that readies the home, before
// the app's pod exists.
func TestHomeStepStreamsThePrepareJob(t *testing.T) {
	apitest.FastPoll(t)
	prep := apitest.HomePreparePod("alice-aaaaaaaa", "javo-prepare-home-alice-x1", "uid-prep")
	prep.Spec.Containers = []corev1.Container{{Name: "prepare"}}
	prep.Status.Phase = corev1.PodSucceeded
	prep.Status.ContainerStatuses = []corev1.ContainerStatus{apitest.Terminated("prepare", 0, "Completed", "")}
	prep.CreationTimestamp = metav1.NewTime(apitest.T0)
	logs := apitest.NewScriptLogs()
	logs.Bodies[apitest.LogKey(prep.Name, "prepare", false)] = "2026-10-30T12:00:01Z made /home/alice\n"
	f := apitest.New(t, apitest.Options{Logs: logs}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"), gatewaytest.Template("streamlit", "streamlit"), prep)
	recs, _ := apitest.StepStream(t, f, "app", model.ConditionHomeReady)
	apitest.Expect(t, recs, "start prepare", "line prepare made /home/alice", "exit prepare exit=0 (Completed)")
	apitest.ExpectEnd(t, recs)
}

// Steps whose work writes no log answer an empty stream; an unknown step is
// refused.
func TestStepStreamOfAStepWithoutContainers(t *testing.T) {
	apitest.FastPoll(t)
	f := apitest.New(t, apitest.Options{Logs: apitest.NewScriptLogs()}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"), gatewaytest.Template("streamlit", "streamlit"), gatewaytest.PodWith("app"))
	recs, _ := apitest.StepStream(t, f, "app", model.ConditionEnvReady)
	apitest.ExpectEnd(t, recs)
	if w := f.Do("GET", "/api/workspaces/app/steps/Nope/log", "", f.Session("alice-aaaaaaaa")); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown step: %d", w.Code)
	}
	if w := f.Do("GET", "/api/workspaces/app/steps/Started/log", "", f.Session("bob-bbbbbbbb")); w.Code != http.StatusNotFound {
		t.Errorf("another person's app: %d", w.Code)
	}
}

// While the home moves to another storage mode its copy Job's output is the
// home step's too, after the Jobs that readied it.
func TestHomeStepStreamsTheStorageCopy(t *testing.T) {
	apitest.FastPoll(t)
	prep := apitest.HomePreparePod("alice-aaaaaaaa", "javo-prepare-home-alice-x1", "uid-prep")
	prep.Spec.Containers = []corev1.Container{{Name: "prepare"}}
	prep.Status.Phase = corev1.PodSucceeded
	prep.Status.ContainerStatuses = []corev1.ContainerStatus{apitest.Terminated("prepare", 0, "Completed", "")}
	prep.CreationTimestamp = metav1.NewTime(apitest.T0)
	cp := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "javo-migrate-x-a1-abc", Namespace: gatewaytest.Namespace, UID: "uid-copy",
		Labels:            map[string]string{model.LabelRole: model.RoleHomeMigrate, model.LabelOwner: "alice-aaaaaaaa"},
		CreationTimestamp: metav1.NewTime(apitest.T0.Add(time.Hour))}}
	cp.Spec.Containers = []corev1.Container{{Name: "copy"}}
	cp.Status.ContainerStatuses = []corev1.ContainerStatus{apitest.RunningCS("copy", 0)}
	logs := apitest.NewScriptLogs()
	logs.Bodies[apitest.LogKey(prep.Name, "prepare", false)] = ""
	copying := logs.Follow(apitest.LogKey(cp.Name, "copy", false))
	f := apitest.New(t, apitest.Options{Logs: logs}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"), gatewaytest.Template("streamlit", "streamlit"), prep, cp)
	recs, _ := apitest.StepStream(t, f, "app", model.ConditionHomeReady)
	apitest.Expect(t, recs, "start prepare", "exit prepare exit=0 (Completed)", "start copy")
	io.WriteString(copying, "2026-10-30T13:00:01Z copied 1200 of 5000 files\n")
	apitest.Expect(t, recs, "line copy copied 1200 of 5000 files")
}
