package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// loginBundle is the bundle a file helper reads rclone's password from
// (its value passwordKey), kept by Logins.
const loginBundle = "rclone"

// helperPrefix starts the name of each person's file helper. No app can
// have such a name: an app's is "<person>-<app>", and neither a person's
// name ends nor an app's begins with a dash. The helper's network and
// containers are named after it, so none is ever an app's.
const helperPrefix = "files--"

func helperName(userSlug string) string { return helperPrefix + userSlug }

// Logins keeps the password of each file helper, in this process's memory
// and nowhere else: not in the database, not on disk and not on a label.
// It is the executor.Secrets of the executor that runs the helpers, which
// gives the password to rclone as a variable when it creates the
// container. A gateway that starts again knows none of them and replaces
// the helpers it finds (Helpers.Ensure).
type Logins struct {
	mu   sync.Mutex
	pass map[string]string
}

var _ executor.Secrets = (*Logins)(nil)

// NewLogins returns an empty Logins.
func NewLogins() *Logins { return &Logins{pass: map[string]string{}} }

func loginKey(of workload.Ref) string { return of.Namespace + "/" + of.Name }

// Get is the helper's password as its one bundle; nil for any other
// bundle and for a helper that has none.
func (l *Logins) Get(_ context.Context, of workload.Ref, name string) (*executor.Bundle, error) {
	pass, ok := l.of(of)
	if name != loginBundle || !ok {
		return nil, nil
	}
	return &executor.Bundle{Data: map[string][]byte{passwordKey: []byte(pass)}}, nil
}

// Put refuses: nothing but a helper's password is kept here, and only
// Helpers sets one.
func (l *Logins) Put(context.Context, workload.Ref, string, executor.Bundle) error {
	return errors.New("files: only a file helper's password is kept here")
}

// Delete forgets the helper's password.
func (l *Logins) Delete(_ context.Context, of workload.Ref, name string) error {
	if name == loginBundle {
		l.forget(of)
	}
	return nil
}

func (l *Logins) of(ref workload.Ref) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pass, ok := l.pass[loginKey(ref)]
	return pass, ok
}

func (l *Logins) set(ref workload.Ref, pass string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pass[loginKey(ref)] = pass
}

func (l *Logins) forget(ref workload.Ref) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.pass, loginKey(ref))
}

// HomeVolumes says whose home a volume is; dockerhome.Volumes is the
// implementation.
type HomeVolumes interface {
	// Owned reports whether the volume called name exists (found) and is
	// owner's home in namespace (theirs).
	Owned(ctx context.Context, namespace, owner, name string) (found, theirs bool, err error)
}

// HelpersConfig configures NewHelpers.
type HelpersConfig struct {
	// Exec runs the helpers, and nothing else: Reap stops what it lists
	// under a helper's name, and the app controller's sweep would stop the
	// helpers of an executor it shared. On Docker it is a docker.Executor
	// of its own, on a Client whose Labels no other executor's has.
	// Required.
	Exec executor.Executor
	// Logins is where Exec reads its workloads' values from (its
	// docker.Options.Secrets). Required.
	Logins *Logins
	// Volumes says whether the volume a home's row names is that person's
	// home; nothing else is ever mounted. Required.
	Volumes HomeVolumes
	// Namespace is where homes are unless a call's context says otherwise
	// (WithNamespace).
	Namespace string
	// Image is the rclone image, pinned by digest.
	Image string
	// UIDs gives the uid and gid the owner's apps run as; the helper runs
	// as them too, so a file it writes belongs to the owner. Required.
	UIDs UserIDs
	// Storage resolves the owner's home. Required.
	Storage Storage
	// IdleAfter is how long a helper lives after its user was last active
	// (Ensure or Touch); zero means 10 minutes.
	IdleAfter time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Helpers is the HomePods of homes that are volumes of a Docker daemon
// (the Docker executor): per person, a container that runs `rclone rcd`
// as that person with their home volume mounted, as a file pod does on
// Kubernetes, from the same plan (fileSpec). It runs through an executor,
// so it is confined as every workload is: on a network of its own that
// reaches nothing and that only the gateway is on, with no port of the
// host, a root it cannot write and a memory limit.
//
// The gateway therefore never mounts a home and never runs as root, and a
// link in a home that leads out of it leads into the helper's own files,
// which hold nothing of anyone's.
type Helpers struct {
	cfg HelpersConfig
	use activity
	// changing lets one call at a time start or stop a person's helper,
	// so a helper always runs with the password Logins holds for it.
	changing fileLocks
}

var _ HomePods = (*Helpers)(nil)

// NewHelpers returns a file helper manager. Call Run to stop idle
// helpers.
func NewHelpers(cfg HelpersConfig) (*Helpers, error) {
	if cfg.Exec == nil || cfg.Logins == nil || cfg.Volumes == nil || cfg.Namespace == "" || cfg.Image == "" || cfg.Storage == nil || cfg.UIDs == nil {
		return nil, errors.New("files: executor, logins, volumes, namespace, image, storage and uids are required")
	}
	if cfg.IdleAfter == 0 {
		cfg.IdleAfter = defaultIdle
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Helpers{cfg: cfg}, nil
}

// Ensure returns "<user>:<password>@<host>:5572", the helper's rclone
// login and address, when the user's helper answers. Otherwise it asks
// for the user's home if they have none (as starting an app does), starts
// the helper once the home's volume was handed to its owner, and returns
// ErrHomeStarting, ErrHomeBusy or ErrHomeConflict. The volume the home's
// row names must be labelled as this user's home, checked on every call.
func (h *Helpers) Ensure(ctx context.Context, userID string) (string, error) {
	h.Touch(userID)
	ns := namespaceOr(ctx, h.cfg.Namespace)
	in, err := homeInput(ctx, h.cfg.Storage, ns, userID)
	if err != nil {
		return "", err
	}
	switch found, theirs, err := h.cfg.Volumes.Owned(ctx, ns, userID, in.Home.Status.ClaimName); {
	case err != nil:
		return "", err
	case !found:
		return "", ErrHomeStarting
	case !theirs:
		return "", ErrHomeConflict
	}
	if err := runAs(ctx, h.cfg.UIDs, userID, &in); err != nil {
		return "", err
	}
	s, err := helperSpec(userID, ns, h.cfg.Image, in)
	if err != nil {
		return "", err
	}

	unlock, err := h.changing.lock(ctx, loginKey(s.Ref))
	if err != nil {
		return "", err
	}
	defer unlock()
	st, err := h.cfg.Exec.Observe(ctx, s.Ref)
	if err != nil {
		return "", err
	}
	if held(in.Home) {
		// A restore or a move waits for everything of the owner's to go,
		// the helper too: it writes to the home.
		if st != nil {
			if err := h.stop(ctx, s.Ref); err != nil {
				return "", err
			}
		}
		return "", ErrHomeBusy
	}
	if st == nil {
		if !in.Home.Status.Prepared {
			// As for an app: nothing mounts a home that still belongs to
			// root.
			return "", ErrHomeStarting
		}
		h.cfg.Logins.set(s.Ref, newPassword())
		if err := h.cfg.Exec.Start(ctx, s); err != nil {
			h.cfg.Logins.forget(s.Ref)
			return "", err
		}
		return "", ErrHomeStarting
	}
	pass, known := h.cfg.Logins.of(s.Ref)
	// A helper whose password this gateway does not hold (an earlier
	// gateway started it), one built from another plan (other ids, another
	// volume or image) and one that failed are replaced.
	if !known || st.Fingerprint.Layout != s.Fingerprint.Layout || st.Phase == "Failed" {
		if err := h.stop(ctx, s.Ref); err != nil {
			return "", err
		}
		return "", ErrHomeStarting
	}
	if st.Phase != "Running" {
		return "", ErrHomeStarting
	}
	addr, err := h.cfg.Exec.Addr(ctx, s.Ref, rcPort)
	if err != nil {
		return "", err
	}
	return url.UserPassword(rcUser, pass).String() + "@" + addr, nil
}

// EnsureGroup is Ensure. Group folders are not offered on Docker, so one
// asked for is ErrNotFound.
func (h *Helpers) EnsureGroup(ctx context.Context, userID, dir string) (string, error) {
	if dir != "" {
		return "", ErrNotFound
	}
	return h.Ensure(ctx, userID)
}

// helperSpec is the plan of userSlug's file helper: fileSpec, with
// rclone's password read from the helper's bundle. Its fingerprint covers
// the whole plan, so a helper built from another one is told apart.
func helperSpec(userSlug, namespace, image string, in podstorage.Input) (workload.Spec, error) {
	// No group folder is ready on Docker; none is mounted either way.
	in.Folders = nil
	s := fileSpec(helperName(userSlug), userSlug, namespace, image, in)
	s.Main.SetEnv(workload.Env{Name: envRCPass, From: &workload.SecretRef{Bundle: loginBundle, Key: passwordKey}})
	plan, err := json.Marshal(s)
	if err != nil {
		return workload.Spec{}, err
	}
	sum := sha256.Sum256(plan)
	s.Fingerprint.Layout = hex.EncodeToString(sum[:])
	return s, nil
}

// stop ends the helper ref and forgets its password.
func (h *Helpers) stop(ctx context.Context, ref workload.Ref) error {
	err := h.cfg.Exec.Stop(ctx, ref)
	h.cfg.Logins.forget(ref)
	return err
}

// Touch records that the user is active, so their helper (if any) is
// kept for another IdleAfter. It never starts one.
func (h *Helpers) Touch(userID string) { h.use.touch(userID, h.cfg.Now()) }

// Release stops the user's helper, if any.
func (h *Helpers) Release(ctx context.Context, userID string) error {
	h.use.forget(userID)
	return h.end(ctx, workload.Ref{Namespace: namespaceOr(ctx, h.cfg.Namespace), Name: helperName(userID), Owner: userID})
}

// end stops the helper ref, after whatever is starting it.
func (h *Helpers) end(ctx context.Context, ref workload.Ref) error {
	unlock, err := h.changing.lock(ctx, loginKey(ref))
	if err != nil {
		return err
	}
	defer unlock()
	return h.stop(ctx, ref)
}

// Reap stops the helpers unused for IdleAfter. One this gateway has not
// served (for example after a restart) counts as used when it was made;
// what is left of one that never ran counts as idle. Nothing that does
// not have a helper's name is stopped.
func (h *Helpers) Reap(ctx context.Context) error {
	all, err := h.cfg.Exec.List(ctx)
	if err != nil {
		return err
	}
	var refs []workload.Ref
	var made []time.Time
	var errs []error
	for _, ref := range all {
		if ref.Owner == "" || ref.Name != helperName(ref.Owner) {
			continue
		}
		st, err := h.cfg.Exec.Observe(ctx, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var created time.Time
		if st != nil {
			created = st.Created
		}
		refs, made = append(refs, ref), append(made, created)
	}
	idle := h.use.idle(h.cfg.Now(), h.cfg.IdleAfter, len(refs), func(i int) (string, time.Time) {
		return refs[i].Owner, made[i]
	})
	for _, i := range idle {
		errs = append(errs, h.end(ctx, refs[i]))
	}
	return errors.Join(errs...)
}

// Run stops idle helpers every minute until ctx is done.
func (h *Helpers) Run(ctx context.Context) { reapLoop(ctx, "reap file helpers", h.Reap) }
