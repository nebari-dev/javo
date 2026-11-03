package helm

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// allows reports whether rules allow verb on resource.
func allows(rules []rbacv1.PolicyRule, resource, verb string) bool {
	return slices.ContainsFunc(rules, func(r rbacv1.PolicyRule) bool {
		return slices.Contains(r.Resources, resource) && slices.Contains(r.Verbs, verb)
	})
}

// importGrants is what the server's account may do in namespace ns (a Role
// bound there) and cluster-wide, as the chart renders it.
func importGrants(t *testing.T, manifest, ns string) (inNS, cluster []rbacv1.PolicyRule) {
	t.Helper()
	roles, clusterRoles := map[string][]rbacv1.PolicyRule{}, map[string][]rbacv1.PolicyRule{}
	objs := docs(t, manifest)
	for _, o := range objs {
		switch r := o.(type) {
		case *rbacv1.Role:
			roles[r.Namespace+"/"+r.Name] = r.Rules
		case *rbacv1.ClusterRole:
			clusterRoles[r.Name] = r.Rules
		}
	}
	server := func(s []rbacv1.Subject) bool {
		return slices.ContainsFunc(s, func(s rbacv1.Subject) bool { return s.Name == "javo-controller" })
	}
	for _, o := range objs {
		switch b := o.(type) {
		case *rbacv1.RoleBinding:
			if b.Namespace == ns && server(b.Subjects) {
				inNS = append(inNS, roles[b.Namespace+"/"+b.RoleRef.Name]...)
			}
		case *rbacv1.ClusterRoleBinding:
			if server(b.Subjects) {
				cluster = append(cluster, clusterRoles[b.RoleRef.Name]...)
			}
		}
	}
	return inNS, cluster
}

// Importing JupyterHub homes needs the server to make claims and Jobs in
// JupyterHub's namespace and to move a volume between namespaces. The chart
// grants that only when jupyterhubImport.namespace names the namespace, and
// tells the import which image its Jobs run.
func TestJupyterHubImportAccessIsOnlyGrantedWhenAsked(t *testing.T) {
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	inNS, cluster := importGrants(t, out, "jupyterhub")
	if len(inNS) != 0 || allows(cluster, "persistentvolumes", "patch") {
		t.Fatalf("granted without being asked: %v %v", inNS, cluster)
	}

	out, err = render(t, "--set", "jupyterhubImport.namespace=jupyterhub", "--set", "image.repository=registry.example.org/javo", "--set", "image.tag=1.2.3")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	inNS, cluster = importGrants(t, out, "jupyterhub")
	for _, want := range []struct{ resource, verb string }{
		{"persistentvolumeclaims", "create"}, {"persistentvolumeclaims", "delete"}, {"jobs", "create"}, {"jobs", "delete"}, {"pods", "list"}, {"pods/log", "get"},
	} {
		if !allows(inNS, want.resource, want.verb) {
			t.Errorf("may not %s %s in jupyterhub", want.verb, want.resource)
		}
	}
	if !allows(cluster, "persistentvolumes", "patch") || allows(cluster, "persistentvolumes", "delete") {
		t.Errorf("volume rules: %v", cluster)
	}
	inApps, _ := importGrants(t, out, "javo-apps")
	if !allows(inApps, "pods/log", "get") {
		t.Error("may not follow the copy Job's log in javo-apps")
	}
	var dep appsv1.Deployment
	if !object(t, out, "Deployment", "javo", &dep) {
		t.Fatal("no Deployment javo")
	}
	var image string
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "JAVO_IMAGE" {
			image = e.Value
		}
	}
	if image != dep.Spec.Template.Spec.Containers[0].Image || image == "" {
		t.Errorf("JAVO_IMAGE = %q, container image %q", image, dep.Spec.Template.Spec.Containers[0].Image)
	}
}
