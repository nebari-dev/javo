package workspace

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// planOf is the app's own part of its workload (planRun), for tests that
// add a step to it before reading the Pod.
func planOf(ws *model.App, t *model.Template, baseURL, publicHost string, layout podstorage.Layout, size sizes.Size, img Images) (workload.Spec, error) {
	return planRun(Inputs{App: ws, Template: t, BaseURL: baseURL, PublicHost: publicHost, Layout: layout, Size: size, Images: img}, containerPlaces)
}

// podOf is the Pod Kubernetes runs s as.
func podOf(s workload.Spec) *corev1.Pod { return kube.Render(s, podspec.Isolation{}) }

// BuildPod is the Pod of the app's own part of its workload.
func BuildPod(ws *model.App, t *model.Template, baseURL, publicHost string, layout podstorage.Layout, size sizes.Size, img Images) (*corev1.Pod, error) {
	s, err := planOf(ws, t, baseURL, publicHost, layout, size, img)
	if err != nil {
		return nil, err
	}
	return podOf(s), nil
}
