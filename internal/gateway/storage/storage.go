// Package storage serves people's files and storage: home and group folder
// listings (with their short cache), raw file reads, what a person's files
// are on, and the admin's storage report.
package storage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/migrate"
	"github.com/nebari-dev/javo/internal/storage/placement"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/storage/report"
	"github.com/nebari-dev/javo/internal/store"
)

// HomeUsages reads how full a home is; files.Lister implements it.
type HomeUsages interface {
	Usage(ctx context.Context, userID string, sizeEnforced bool) (files.HomeUsage, error)
}

// StorageFolder is one group folder the caller can see.
type StorageFolder struct {
	Group  string `json:"group"`
	Dir    string `json:"dir" doc:"Folder name under /shared"`
	Ready  bool   `json:"ready"`
	Reason *struct {
		Code  string `json:"code"`
		Title string `json:"title"`
		What  string `json:"what"`
		Todo  string `json:"todo"`
	} `json:"reason,omitempty"`
}

// StorageView is what the caller's files are on, in plain words.
type StorageView struct {
	Mode         string           `json:"mode" enum:"perUser,shared"`
	ModeLabel    string           `json:"modeLabel" doc:"The sidebar line"`
	SizeEnforced bool             `json:"sizeEnforced"`
	Size         int64            `json:"size" doc:"Home size in bytes; 0 when none is set yet"`
	Used         *int64           `json:"used,omitempty" doc:"Bytes used; only with usage=true and where the size is enforced"`
	ScratchPaths []string         `json:"scratchPaths"`
	Notices      []reasons.Notice `json:"notices"`
	Folders      []StorageFolder  `json:"folders"`
}

type storageInput struct {
	Usage bool   `query:"usage" doc:"Also read the space used; starts the file pod if it is not running"`
	Team  string `query:"team" maxLength:"63"`
}

type storageOutput struct{ Body StorageView }

// PlacementView says where a new app would run.
type PlacementView struct {
	CoLocated   bool            `json:"coLocated"`
	Node        string          `json:"node,omitempty"`
	FreeCPU     float64         `json:"freeCPU" doc:"CPU cores free on the node, approximate: only app pods are counted"`
	FreeMemory  int64           `json:"freeMemory" doc:"Bytes of memory free on the node, approximate: only app pods are counted"`
	Fits        bool            `json:"fits"`
	RunningApps []string        `json:"runningApps" doc:"Display names of the caller's apps on the node"`
	Notice      *reasons.Notice `json:"notice,omitempty" doc:"The banner to show; set when CoLocated"`
}

type placementInput struct {
	Template string `query:"template" minLength:"1" maxLength:"63"`
	Size     string `query:"size" maxLength:"63"`
	Team     string `query:"team" maxLength:"63"`
}

type placementOutput struct{ Body PlacementView }

// adminStorageOutput is the read-only storage report. Storage is set at
// deploy time; tests and moves run from the javo CLI.
type adminStorageOutput struct {
	Body struct {
		Summary string       `json:"summary" doc:"Where files are, in one line; says when nothing needs attention"`
		Rows    []report.Row `json:"rows"`
	}
}

// API serves what the caller's files are on (/api/storage), where a new
// app would run, and the admin's storage report.
type API struct {
	DB *store.Store
	// spec is the platform config's storage section, defaults applied.
	Spec model.ClusterStorageSpec
	// c reads pods and nodes, for the placement preview.
	Client     client.Client
	Sizes      sizes.Catalog
	Namespaces *namespaces.Finder
	Roles      config.Roles
	Usage      HomeUsages
	Groups     podstorage.Memberships
	Now        func() time.Time
}

// Register adds the storage routes.
func (s *API) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "getStorage", Method: http.MethodGet, Path: "/api/storage",
		Summary: "What the caller's files are on", Tags: []string{"storage"},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, s.get)
	huma.Register(api, huma.Operation{
		OperationID: "previewPlacement", Method: http.MethodGet, Path: "/api/storage/placement",
		Summary: "Where a new app would run", Tags: []string{"storage"},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, s.placement)

	admin := func(id, method, path, summary string) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"storage"},
			Errors: []int{http.StatusUnprocessableEntity},
		}
	}
	authn.RegisterAdmin(api, s.Roles, admin("getAdminStorage", http.MethodGet, "/api/admin/storage", "Storage capability report"),
		func(ctx context.Context, _ *struct{}) (*adminStorageOutput, error) {
			cs, err := s.clusterStorage(ctx)
			if err != nil {
				return nil, problem.Internal("read the cluster's storage", err)
			}
			homes, err := s.DB.Homes().List(ctx)
			if err != nil {
				return nil, problem.Internal("list homes", err)
			}
			out := &adminStorageOutput{}
			out.Body.Rows = report.Build(cs, homes)
			out.Body.Summary = report.Summary(cs, out.Body.Rows)
			return out, nil
		})
}

// clusterStorage is the storage view: the config, the admin's requests and
// the findings (empty before the controller has looked at the cluster).
func (s *API) clusterStorage(ctx context.Context) (*model.ClusterStorage, error) {
	cs, err := s.DB.Storage().Cluster(ctx, s.Spec)
	return &cs, err
}

// home returns the caller's home, nil before it is made.
func (s *API) home(ctx context.Context, ns, owner string) (*model.Home, error) {
	h, err := s.DB.Homes().ByOwner(ctx, ns, owner)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return &h, err
}

// mode is the mode the caller's files are on: the Home's, else the one the
// cluster chose for new homes, else a private disk.
func mode(h *model.Home, cs *model.ClusterStorage) model.HomeMode {
	switch {
	case h != nil && h.Status.Mode != "":
		return h.Status.Mode
	case cs.Status.Choice != nil && cs.Status.Choice.Mode != "":
		return cs.Status.Choice.Mode
	}
	return model.HomeModePerUser
}

func (s *API) namespace(p authz.Principal, team string) (string, error) {
	ns, err := s.Namespaces.For(p, team)
	if err != nil {
		return "", problem.Team(err)
	}
	return ns, nil
}

func (s *API) get(ctx context.Context, in *storageInput) (*storageOutput, error) {
	p := authn.User(ctx)
	ns, err := s.namespace(p, in.Team)
	if err != nil {
		return nil, err
	}
	cs, err := s.clusterStorage(ctx)
	if err != nil {
		return nil, problem.Internal("read the cluster's storage", err)
	}
	h, err := s.home(ctx, ns, p.ID)
	if err != nil {
		return nil, problem.Internal("read home", err)
	}
	m := mode(h, cs)
	v := StorageView{Mode: string(m), ScratchPaths: podstorage.ScratchPaths(), Notices: []reasons.Notice{}, Folders: []StorageFolder{}}
	// Notices are only for what limits the person or asks something of
	// them; how storage works when nothing does is not news.
	if m == model.HomeModeShared {
		v.ModeLabel = reasons.Msg(reasons.MsgHomeLabelShared)
	} else {
		v.ModeLabel = reasons.Msg(reasons.MsgHomeLabelPerUser)
	}

	size := cs.Spec.Home.Size
	if h != nil {
		v.SizeEnforced = h.Status.SizeEnforced
		if h.Spec.Size != nil {
			size = h.Spec.Size
		}
	} else if cs.Status.Choice != nil {
		if i := slices.IndexFunc(cs.Status.Classes, func(c model.ClassCapability) bool { return c.Name == cs.Status.Choice.HomeClass }); i >= 0 {
			v.SizeEnforced = cs.Status.Classes[i].SizeEnforced.Value
		}
	}
	if size != nil {
		v.Size = size.Value()
	}
	// Shown on the app page only, where it says which folders an app loses.
	v.Notices = append(v.Notices, reasons.NoticeFor(reasons.ScratchPaths))
	// A move of the caller's files comes first: it is the one notice with a
	// date, and the UI shows only the first banner of its tone.
	if n := s.migrationNotice(h, cs, v.Size); n != nil {
		v.Notices = slices.Insert(v.Notices, 0, *n)
	}

	if in.Usage && v.SizeEnforced && s.Usage != nil {
		u, err := s.Usage.Usage(files.WithNamespace(ctx, ns), p.ID, true)
		switch {
		case errors.Is(err, files.ErrHomeStarting), errors.Is(err, files.ErrHomeBusy):
			// Expected while the file pod starts; the page asks again.
		case err != nil:
			slog.Error("read home usage", "user", p.ID, "err", err)
		case u.Enforced && u.Total > 0:
			v.Used = &u.Used
			v.Size = u.Total
		}
	}
	if v.Folders, err = s.folders(ctx, ns, p.ID); err != nil {
		return nil, problem.Internal("list shared folders", err)
	}
	return &storageOutput{Body: v}, nil
}

// migrationNotice is the banner for a move of the caller's home: from the
// moment an admin schedules it, while it runs, and when it failed.
func (s *API) migrationNotice(h *model.Home, cs *model.ClusterStorage, size int64) *reasons.Notice {
	if h == nil {
		return nil
	}
	if m := h.Status.Migration; m != nil {
		switch m.Phase {
		case model.MigrationWaiting, model.MigrationCopying, model.MigrationVerifying:
			n := reasons.NoticeFor(reasons.MigrationRunning)
			return &n
		case model.MigrationFailed:
			n := reasons.NoticeFor(reasons.NoticeMigrationFailed)
			return &n
		}
	}
	at, to := cs.Spec.Migration.StartAt, cs.Spec.Migration.To
	if at == nil || at.IsZero() || cs.Status.Choice == nil || to != cs.Status.Choice.Mode || h.Status.Mode == to {
		return nil
	}
	n := reasons.NoticeFor(reasons.MigrationScheduled,
		reasons.ModeWords(to == model.HomeModeShared), reasons.FormatWhen(at.Time), migrate.StopMinutes(size))
	return &n
}

// folders lists the group folders of the caller's groups, by directory.
func (s *API) folders(ctx context.Context, ns, owner string) ([]StorageFolder, error) {
	out := []StorageFolder{}
	if s.Groups == nil {
		return out, nil
	}
	groups, err := s.Groups.Groups(ctx, owner)
	if err != nil || len(groups) == 0 {
		return out, err
	}
	all, err := s.DB.SharedFolders().List(ctx)
	if err != nil {
		return nil, err
	}
	all = slices.DeleteFunc(all, func(f model.SharedFolder) bool { return f.Namespace != ns })
	for _, f := range podstorage.FolderPerDir(all) {
		if !slices.Contains(groups, f.Spec.Group) {
			continue
		}
		sf := StorageFolder{Group: f.Spec.Group, Dir: naming.GroupDir(f.Spec.Group), Ready: podstorage.FolderReady(f)}
		if !sf.Ready {
			r, _ := reasons.Lookup(podstorage.FolderReason(f))
			sf.Reason = &struct {
				Code  string `json:"code"`
				Title string `json:"title"`
				What  string `json:"what"`
				Todo  string `json:"todo"`
			}{string(r.Code), r.Title, r.What, r.Todo}
		}
		out = append(out, sf)
	}
	return out, nil
}

func (s *API) placement(ctx context.Context, in *placementInput) (*placementOutput, error) {
	p := authn.User(ctx)
	t, err := templates.Usable(ctx, s.DB.Templates(), s.Roles, p, in.Template)
	if err != nil {
		return nil, err
	}
	sz, ok := s.Sizes.Resolve(in.Size)
	if !ok {
		return nil, problem.FieldError("size", reasons.MsgSizeUnavailable)
	}
	ns, err := s.namespace(p, in.Team)
	if err != nil {
		return nil, err
	}
	cs, err := s.clusterStorage(ctx)
	if err != nil {
		return nil, problem.Internal("read the cluster's storage", err)
	}
	h, err := s.home(ctx, ns, p.ID)
	if err != nil {
		return nil, problem.Internal("read home", err)
	}
	out := &placementOutput{}
	out.Body.RunningApps = []string{}
	if mode(h, cs) != model.HomeModePerUser || cs.Status.Nodes <= 1 || (cs.Status.Choice != nil && cs.Status.Choice.SingleNode) {
		return out, nil
	}

	req := placement.Request{CPU: t.Spec.Resources.Requests[corev1.ResourceCPU], Memory: t.Spec.Resources.Requests[corev1.ResourceMemory]}
	if !sz.CPU.IsZero() {
		req.CPU, req.Memory = sz.CPU, sz.Memory
	}
	// The owner's own pods say which node there is to be next to; only that
	// node's pods are then read, to count what is free there.
	var mine corev1.PodList
	if err := s.Client.List(ctx, &mine, client.InNamespace(ns), client.MatchingLabels{model.LabelOwner: p.ID}); err != nil {
		return nil, problem.Internal("list pods", err)
	}
	i := slices.IndexFunc(mine.Items, func(p corev1.Pod) bool { return p.Spec.NodeName != "" && p.Labels[model.LabelApp] != "" })
	if i < 0 {
		return out, nil
	}
	all, err := s.appPodsOnNode(ctx, mine.Items[i].Spec.NodeName)
	if err != nil {
		return nil, problem.Internal("list pods", err)
	}
	var nodes corev1.NodeList
	if err := s.Client.List(ctx, &nodes); err != nil {
		return nil, problem.Internal("list nodes", err)
	}
	res := placement.Preview(nodes.Items, all, ns, p.ID, req)
	if !res.CoLocated {
		return out, nil
	}
	names, err := s.displayNames(ctx, p.ID)
	if err != nil {
		return nil, problem.Internal("list apps", err)
	}
	b := &out.Body
	b.CoLocated, b.Node, b.Fits = true, res.Node, res.Fits
	b.FreeCPU, b.FreeMemory = float64(res.FreeCPU.MilliValue())/1000, res.FreeMemory.Value()
	for _, o := range res.RunningApps {
		b.RunningApps = append(b.RunningApps, cmp.Or(names[o], o))
	}
	slices.SortFunc(b.RunningApps, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	code := reasons.PlacementWaits
	if b.Fits {
		code = reasons.PlacementFits
	}
	apps := fmt.Sprintf("%d apps", len(b.RunningApps))
	if len(b.RunningApps) == 1 {
		apps = "1 app"
	}
	n := reasons.NoticeFor(code, b.Node, apps, strconv.FormatFloat(b.FreeCPU, 'f', -1, 64))
	b.Notice = &n
	return out, nil
}

// appPodsOnNode lists the pods on node in the namespaces that hold apps. The
// gateway is not granted to read pods elsewhere (it faces the internet, and a
// pod spec can carry secrets in env values), so other workloads on the node
// are not counted and the free figures are approximate. With one namespace
// per person the chart already grants a cluster-wide read, so one call
// replaces a call per namespace.
func (s *API) appPodsOnNode(ctx context.Context, node string) ([]corev1.Pod, error) {
	nss, err := s.Namespaces.All(ctx)
	if err != nil {
		return nil, err
	}
	onNode := client.MatchingFields{"spec.nodeName": node}
	if !s.Namespaces.Listed() {
		var l corev1.PodList
		if err := s.Client.List(ctx, &l, onNode); err != nil {
			return nil, err
		}
		return slices.DeleteFunc(l.Items, func(p corev1.Pod) bool { return !slices.Contains(nss, p.Namespace) }), nil
	}
	var out []corev1.Pod
	for _, ns := range nss {
		var l corev1.PodList
		if err := s.Client.List(ctx, &l, client.InNamespace(ns), onNode); err != nil {
			return nil, err
		}
		out = append(out, l.Items...)
	}
	return out, nil
}

// displayNames maps the owner's app ids (the label on their pods) to the
// apps' display names.
func (s *API) displayNames(ctx context.Context, owner string) (map[string]string, error) {
	apps, err := s.DB.Apps().List(ctx, owner, true)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(apps))
	for _, a := range apps {
		m[a.ID] = cmp.Or(a.DisplayName, a.Name)
	}
	return m, nil
}
