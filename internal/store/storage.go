package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Change tables of the storage rows. Home and folder writes are announced,
// status included: the app controller waits on a home, and the storage
// controllers wait on each other's findings.
const (
	TableHomes           = "homes"
	TableSharedFolders   = "shared_folders"
	TableStorageSettings = "storage_settings"
	// TableStorageStatus announces a write of the cluster's storage findings
	// (the cluster_facts keys below); its id is the key.
	TableStorageStatus = "storage_status"
)

const (
	factStorage      = "storage"
	factStorageProbe = "storage.probe"
	settingRequests  = "requests"
)

// Homes is the homes table. The controller keeps each row's status; the
// gateway adds a person's missing home (Ensure, from the Files endpoints)
// and records admins' backup and restore requests; the javo CLI changes
// sizes and deletes homes.
type Homes struct{ s *Store }

// Homes returns the homes table.
func (s *Store) Homes() *Homes { return &Homes{s} }

// Ensure returns owner's home in namespace, creating it at version 1 with
// size (nil: the configured default) when there is none. An existing home
// is returned as it is.
func (h *Homes) Ensure(ctx context.Context, namespace, owner string, size *resource.Quantity) (model.Home, error) {
	home := model.Home{Namespace: namespace, Spec: model.HomeSpec{Owner: owner, Size: size}}
	if err := home.Validate(); err != nil {
		return model.Home{}, err
	}
	spec, err := marshal(home.Spec, "{}")
	if err != nil {
		return model.Home{}, err
	}
	var out model.Home
	err = h.s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) {
		n, err := q.InsertHome(ctx, sqlcgen.InsertHomeParams{
			ID: newID(), Namespace: namespace, Owner: owner, Spec: spec, CreatedAt: dbtime.From(now()),
		})
		if err != nil {
			return nil, err
		}
		row, err := q.GetHomeByOwner(ctx, sqlcgen.GetHomeByOwnerParams{Namespace: namespace, Owner: owner})
		if err != nil {
			return nil, err
		}
		if out, err = homeOf(row); err != nil || n == 0 {
			return nil, err
		}
		return []Change{{TableHomes, out.ID}}, nil
	})
	return out, err
}

// Get returns the home with id or ErrNotFound.
func (h *Homes) Get(ctx context.Context, id string) (model.Home, error) {
	row, err := h.s.q.GetHome(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Home{}, ErrNotFound
	} else if err != nil {
		return model.Home{}, err
	}
	return homeOf(row)
}

// ByOwner returns owner's home in namespace or ErrNotFound.
func (h *Homes) ByOwner(ctx context.Context, namespace, owner string) (model.Home, error) {
	row, err := h.s.q.GetHomeByOwner(ctx, sqlcgen.GetHomeByOwnerParams{Namespace: namespace, Owner: owner})
	if errors.Is(err, sql.ErrNoRows) {
		return model.Home{}, ErrNotFound
	} else if err != nil {
		return model.Home{}, err
	}
	return homeOf(row)
}

// List returns every home.
func (h *Homes) List(ctx context.Context) ([]model.Home, error) {
	rows, err := h.s.q.ListHomes(ctx)
	return homesOf(rows, err)
}

// ListByOwner returns owner's homes, one per namespace.
func (h *Homes) ListByOwner(ctx context.Context, owner string) ([]model.Home, error) {
	rows, err := h.s.q.ListHomesByOwner(ctx, owner)
	return homesOf(rows, err)
}

// Update stores next's spec and status if the stored version is still
// version (else ErrConflict) and returns the stored home.
func (h *Homes) Update(ctx context.Context, next model.Home, version int64) (model.Home, error) {
	var out model.Home
	err := h.s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) {
		var err error
		out, err = writeHome(ctx, q, next, version)
		return []Change{{TableHomes, next.ID}}, err
	})
	return out, err
}

// SetSize sets the size a home asks for, at an admin's request; the
// controller grows the claim where the class allows it.
func (h *Homes) SetSize(ctx context.Context, actor model.Actor, id string, size resource.Quantity) (model.Home, error) {
	var out model.Home
	err := h.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := q.GetHome(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return effect{}, ErrNotFound
		} else if err != nil {
			return effect{}, err
		}
		cur, err := homeOf(row)
		if err != nil {
			return effect{}, err
		}
		cur.Spec.Size = &size
		if out, err = writeHome(ctx, q, cur, cur.Version); err != nil {
			return effect{}, err
		}
		return effect{
			audit: &auditRow{action: "home.resize", kind: "home", name: out.Spec.Owner, id: out.ID,
				namespace: out.Namespace, owner: out.Spec.Owner, detail: map[string]string{"size": size.String()}},
			changes: []Change{{TableHomes, out.ID}},
		}, nil
	})
	return out, err
}

// Delete removes the home with id: its row, with its backup and restore
// bookkeeping. Its claim, and so its files, are the caller's to keep or
// delete. A home that is gone is ErrNotFound.
func (h *Homes) Delete(ctx context.Context, actor model.Actor, id string) error {
	return h.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := q.GetHome(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return effect{}, ErrNotFound
		} else if err != nil {
			return effect{}, err
		}
		if _, err := q.DeleteHome(ctx, id); err != nil {
			return effect{}, err
		}
		return effect{
			audit: &auditRow{action: "home.delete", kind: "home", name: row.Owner, id: row.ID,
				namespace: row.Namespace, owner: row.Owner},
			changes: []Change{{TableHomes, row.ID}},
		}, nil
	})
}

// DeleteFor removes every home of owner (Delete) and returns how many.
func (h *Homes) DeleteFor(ctx context.Context, actor model.Actor, owner string) (int, error) {
	homes, err := h.ListByOwner(ctx, owner)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, home := range homes {
		switch err := h.Delete(ctx, actor, home.ID); {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return n, err
		default:
			n++
		}
	}
	return n, nil
}

// RequestBackupRun asks every home and every shared folder to be backed up
// as part of the full backup run run (the backup job's run), at now. A home
// or folder already asked for that run is left. It returns how many of each
// it asked.
func (h *Homes) RequestBackupRun(ctx context.Context, actor model.Actor, run string, now time.Time) (homes, folders int, err error) {
	req := &model.BackupRequest{Run: run, At: metav1.NewTime(now)}
	err = h.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		rows, err := q.ListHomes(ctx)
		if err != nil {
			return effect{}, err
		}
		var changes []Change
		for _, r := range rows {
			cur, err := homeOf(r)
			if err != nil {
				return effect{}, err
			}
			if cur.Spec.Backup != nil && cur.Spec.Backup.Run == run {
				continue
			}
			cur.Spec.Backup = req
			if _, err := writeHome(ctx, q, cur, cur.Version); err != nil {
				return effect{}, err
			}
			changes = append(changes, Change{TableHomes, cur.ID})
			homes++
		}
		frows, err := q.ListSharedFolders(ctx)
		if err != nil {
			return effect{}, err
		}
		for _, r := range frows {
			cur, err := folderOf(r)
			if err != nil {
				return effect{}, err
			}
			if cur.Spec.Backup != nil && cur.Spec.Backup.Run == run {
				continue
			}
			cur.Spec.Backup = req
			spec, err := marshal(cur.Spec, "{}")
			if err != nil {
				return effect{}, err
			}
			status, err := marshal(cur.Status, "{}")
			if err != nil {
				return effect{}, err
			}
			if _, err := q.UpdateSharedFolder(ctx, sqlcgen.UpdateSharedFolderParams{Spec: spec, Status: status, ID: cur.ID, Version: cur.Version}); err != nil {
				return effect{}, err
			}
			changes = append(changes, Change{TableSharedFolders, cur.ID})
			folders++
		}
		if len(changes) == 0 {
			return effect{}, nil
		}
		return effect{
			audit: &auditRow{action: "backup.run", kind: "backup", name: run,
				detail: map[string]int{"homes": homes, "folders": folders}},
			changes: changes,
		}, nil
	})
	return homes, folders, err
}

// ErrRestoreRunning is returned by RequestRestore while the home's last
// restore has not ended.
var ErrRestoreRunning = errors.New("store: a restore of this home is under way")

// RequestRestore asks the controller to bring the home back from a backup,
// at an admin's request at now. It is refused with ErrRestoreRunning while
// the previous request has not ended.
func (h *Homes) RequestRestore(ctx context.Context, actor model.Actor, id string, req model.HomeRestoreRequest, now time.Time) (model.Home, error) {
	var out model.Home
	err := h.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := q.GetHome(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return effect{}, ErrNotFound
		} else if err != nil {
			return effect{}, err
		}
		cur, err := homeOf(row)
		if err != nil {
			return effect{}, err
		}
		if prev := cur.Spec.Restore; prev != nil && (cur.Status.Restore == nil || cur.Status.Restore.ID != prev.ID || !cur.Status.Restore.Ended()) {
			return effect{}, ErrRestoreRunning
		}
		req.ID, req.By, req.At = newID(), actor.Person, metav1.NewTime(now)
		if req.By == "" {
			req.By = actor.System
		}
		cur.Spec.Restore = &req
		if out, err = writeHome(ctx, q, cur, cur.Version); err != nil {
			return effect{}, err
		}
		return effect{
			audit: &auditRow{action: "home.restore", kind: "home", name: out.Spec.Owner, id: out.ID,
				namespace: out.Namespace, owner: out.Spec.Owner,
				detail: map[string]string{"snapshot": req.Snapshot, "snapshotTime": req.SnapshotTime.UTC().Format(time.RFC3339), "mode": req.Mode}},
			changes: []Change{{TableHomes, out.ID}},
		}, nil
	})
	return out, err
}

func writeHome(ctx context.Context, q *sqlcgen.Queries, next model.Home, version int64) (model.Home, error) {
	spec, err := marshal(next.Spec, "{}")
	if err != nil {
		return model.Home{}, err
	}
	status, err := marshal(next.Status, "{}")
	if err != nil {
		return model.Home{}, err
	}
	n, err := q.UpdateHome(ctx, sqlcgen.UpdateHomeParams{Spec: spec, Status: status, ID: next.ID, Version: version})
	if err != nil {
		return model.Home{}, err
	}
	if n == 0 {
		if _, err := q.GetHome(ctx, next.ID); errors.Is(err, sql.ErrNoRows) {
			return model.Home{}, ErrNotFound
		}
		return model.Home{}, ErrConflict
	}
	row, err := q.GetHome(ctx, next.ID)
	if err != nil {
		return model.Home{}, err
	}
	return homeOf(row)
}

func homeOf(r sqlcgen.Home) (model.Home, error) {
	h := model.Home{ID: r.ID, Namespace: r.Namespace, Version: r.Version}
	if err := unmarshal(r.Spec, &h.Spec); err != nil {
		return h, err
	}
	h.Spec.Owner = r.Owner
	return h, unmarshal(r.Status, &h.Status)
}

func homesOf(rows []sqlcgen.Home, err error) ([]model.Home, error) {
	if err != nil {
		return nil, err
	}
	out := make([]model.Home, 0, len(rows))
	for _, r := range rows {
		h, err := homeOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// SharedFolders is the shared_folders table. Admins create and delete
// folders; the controller writes their status.
type SharedFolders struct{ s *Store }

// SharedFolders returns the shared_folders table.
func (s *Store) SharedFolders() *SharedFolders { return &SharedFolders{s} }

// Create stores a new folder; a taken (namespace, name) is ErrExists.
func (f *SharedFolders) Create(ctx context.Context, actor model.Actor, in model.SharedFolder) (model.SharedFolder, error) {
	if err := in.Validate(); err != nil {
		return model.SharedFolder{}, err
	}
	spec, err := marshal(in.Spec, "{}")
	if err != nil {
		return model.SharedFolder{}, err
	}
	id := newID()
	var out model.SharedFolder
	err = f.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.InsertSharedFolder(ctx, sqlcgen.InsertSharedFolderParams{
			ID: id, Namespace: in.Namespace, Name: in.Name, GroupName: in.Spec.Group, Spec: spec, CreatedAt: dbtime.From(now()),
		})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrExists
		}
		row, err := q.GetSharedFolder(ctx, id)
		if err != nil {
			return effect{}, err
		}
		if out, err = folderOf(row); err != nil {
			return effect{}, err
		}
		return folderEffect("folder.create", out), nil
	})
	return out, err
}

func folderEffect(action string, f model.SharedFolder) effect {
	return effect{
		audit: &auditRow{action: action, kind: "folder", name: f.Name, id: f.ID, namespace: f.Namespace,
			detail: map[string]string{"group": f.Spec.Group}},
		changes: []Change{{TableSharedFolders, f.ID}},
	}
}

// Delete removes a folder. Its claim, and the files on it, stay.
func (f *SharedFolders) Delete(ctx context.Context, actor model.Actor, id string) error {
	return f.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := q.GetSharedFolder(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return effect{}, ErrNotFound
		} else if err != nil {
			return effect{}, err
		}
		folder, err := folderOf(row)
		if err != nil {
			return effect{}, err
		}
		if _, err := q.DeleteSharedFolder(ctx, id); err != nil {
			return effect{}, err
		}
		return folderEffect("folder.delete", folder), nil
	})
}

// Get returns the folder with id or ErrNotFound.
func (f *SharedFolders) Get(ctx context.Context, id string) (model.SharedFolder, error) {
	row, err := f.s.q.GetSharedFolder(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SharedFolder{}, ErrNotFound
	} else if err != nil {
		return model.SharedFolder{}, err
	}
	return folderOf(row)
}

// ByName returns the folder name in namespace or ErrNotFound.
func (f *SharedFolders) ByName(ctx context.Context, namespace, name string) (model.SharedFolder, error) {
	row, err := f.s.q.GetSharedFolderByName(ctx, sqlcgen.GetSharedFolderByNameParams{Namespace: namespace, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return model.SharedFolder{}, ErrNotFound
	} else if err != nil {
		return model.SharedFolder{}, err
	}
	return folderOf(row)
}

// List returns every folder, oldest first.
func (f *SharedFolders) List(ctx context.Context) ([]model.SharedFolder, error) {
	rows, err := f.s.q.ListSharedFolders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.SharedFolder, 0, len(rows))
	for _, r := range rows {
		folder, err := folderOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, folder)
	}
	return out, nil
}

// Update stores next's status (the group stays as created) if the stored
// version is still version, else ErrConflict.
func (f *SharedFolders) Update(ctx context.Context, next model.SharedFolder, version int64) (model.SharedFolder, error) {
	var out model.SharedFolder
	err := f.s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) {
		row, err := q.GetSharedFolder(ctx, next.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, err
		}
		cur, err := folderOf(row)
		if err != nil {
			return nil, err
		}
		next.Spec.Group = cur.Spec.Group
		spec, err := marshal(next.Spec, "{}")
		if err != nil {
			return nil, err
		}
		status, err := marshal(next.Status, "{}")
		if err != nil {
			return nil, err
		}
		n, err := q.UpdateSharedFolder(ctx, sqlcgen.UpdateSharedFolderParams{Spec: spec, Status: status, ID: next.ID, Version: version})
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, ErrConflict
		}
		if row, err = q.GetSharedFolder(ctx, next.ID); err != nil {
			return nil, err
		}
		out, err = folderOf(row)
		return []Change{{TableSharedFolders, next.ID}}, err
	})
	return out, err
}

func folderOf(r sqlcgen.SharedFolder) (model.SharedFolder, error) {
	f := model.SharedFolder{ID: r.ID, Namespace: r.Namespace, Name: r.Name, CreatedAt: r.CreatedAt.Time(), Version: r.Version}
	if err := unmarshal(r.Spec, &f.Spec); err != nil {
		return f, err
	}
	f.Spec.Group = r.GroupName
	return f, unmarshal(r.Status, &f.Status)
}

// Storage is the cluster-wide storage state: the admin's run-time requests
// (storage_settings) and the findings (cluster_facts).
type Storage struct{ s *Store }

// Storage returns the cluster-wide storage state.
func (s *Store) Storage() *Storage { return &Storage{s} }

// Requests returns the admin's run-time requests (none at first).
func (st *Storage) Requests(ctx context.Context) (model.StorageRequests, error) {
	r, _, err := readRequests(ctx, st.s.q)
	return r, err
}

func readRequests(ctx context.Context, q *sqlcgen.Queries) (model.StorageRequests, int64, error) {
	var r model.StorageRequests
	row, err := q.GetStorageSetting(ctx, settingRequests)
	if errors.Is(err, sql.ErrNoRows) {
		return r, 0, nil
	} else if err != nil {
		return r, 0, err
	}
	return r, row.Version, unmarshal(row.Value, &r)
}

// UpdateRequests applies fn to the requests and stores the result, audited as
// action. An error from fn writes nothing; a concurrent update is ErrConflict.
func (st *Storage) UpdateRequests(ctx context.Context, actor model.Actor, action string, fn func(*model.StorageRequests) error) error {
	return st.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		r, version, err := readRequests(ctx, q)
		if err != nil {
			return effect{}, err
		}
		if err := fn(&r); err != nil {
			return effect{}, err
		}
		value, err := marshal(r, "{}")
		if err != nil {
			return effect{}, err
		}
		at := dbtime.From(now())
		var n int64
		if version == 0 {
			n, err = q.InsertStorageSetting(ctx, sqlcgen.InsertStorageSettingParams{Key: settingRequests, Value: value, UpdatedAt: at})
		} else {
			n, err = q.UpdateStorageSetting(ctx, sqlcgen.UpdateStorageSettingParams{Value: value, UpdatedAt: at, Key: settingRequests, Version: version})
		}
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrConflict
		}
		return effect{
			audit:   &auditRow{action: action, kind: "storage", name: settingRequests, detail: json.RawMessage(value)},
			changes: []Change{{TableStorageSettings, settingRequests}},
		}, nil
	})
}

// probeParts is the probe controller's part of the status.
type probeParts struct {
	Probe        *model.ProbeStatus  `json:"probe,omitempty"`
	ProbeResults []model.ProbeStatus `json:"probeResults,omitempty"`
}

// Status returns the cluster's storage findings: the cluster controller's
// part with the probe controller's part in place.
func (st *Storage) Status(ctx context.Context) (model.ClusterStorageStatus, error) {
	var out model.ClusterStorageStatus
	if err := st.readFact(ctx, factStorage, &out); err != nil {
		return out, err
	}
	var p probeParts
	if err := st.readFact(ctx, factStorageProbe, &p); err != nil {
		return out, err
	}
	out.Probe, out.ProbeResults = p.Probe, p.ProbeResults
	return out, nil
}

func (st *Storage) readFact(ctx context.Context, key string, v any) error {
	f, err := st.s.Facts().Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	return json.Unmarshal(f.Value, v)
}

// SetClusterStatus writes the cluster controller's part of the findings;
// the probe fields of s are not its to write and are dropped.
func (st *Storage) SetClusterStatus(ctx context.Context, s model.ClusterStorageStatus) error {
	s.Probe, s.ProbeResults = nil, nil
	return st.writeFact(ctx, factStorage, s)
}

// SetProbeStatus writes the probe controller's part of the findings.
func (st *Storage) SetProbeStatus(ctx context.Context, probe *model.ProbeStatus, results []model.ProbeStatus) error {
	return st.writeFact(ctx, factStorageProbe, probeParts{Probe: probe, ProbeResults: results})
}

func (st *Storage) writeFact(ctx context.Context, key string, v any) error {
	value, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return st.s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) {
		err := q.UpsertFact(ctx, sqlcgen.UpsertFactParams{Key: key, Value: string(value), ObservedAt: dbtime.From(now())})
		return []Change{{TableStorageStatus, key}}, err
	})
}

// StorageView is Storage.Cluster for one platform config, as one call.
type StorageView struct {
	Storage *Storage
	// Config is the platform config's storage section, defaults applied.
	Config model.ClusterStorageSpec
}

// Cluster returns the storage view.
func (v StorageView) Cluster(ctx context.Context) (model.ClusterStorage, error) {
	return v.Storage.Cluster(ctx, v.Config)
}

// Cluster puts the storage view together: cfg (the platform config, with
// defaults applied) with the admin's requests, and the findings.
func (st *Storage) Cluster(ctx context.Context, cfg model.ClusterStorageSpec) (model.ClusterStorage, error) {
	r, err := st.Requests(ctx)
	if err != nil {
		return model.ClusterStorage{}, err
	}
	status, err := st.Status(ctx)
	return model.ClusterStorage{Spec: cfg.WithRequests(r), Status: status}, err
}
