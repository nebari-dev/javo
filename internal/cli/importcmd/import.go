// Package importcmd is `javo import`: bring data in from other systems.
package importcmd

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/kubeclient"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/hubimport"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/store"
)

// importPoll is how often the import looks at its homes again.
var importPoll = 2 * time.Second

// FollowJobLog copies the log of the Job's pod to w until it ends; nil
// follows nothing (tests). main sets it to FollowJobLogInCluster.
var FollowJobLog func(ctx context.Context, job types.NamespacedName, w io.Writer) error

// New is `javo import`.
func New(newClient kubeclient.Factory, openStore dbflag.Opener) *cobra.Command {
	imp := &cobra.Command{Use: "import", Short: "Bring data in from other systems"}
	imp.AddCommand(newImportHubHomesCmd(newClient, openStore))
	return imp
}

type hubImportFlags struct {
	from, into, image, subjects  string
	users                        []string
	dryRun, createPeople, noWait bool
	parallel                     int
	stuckAfter                   time.Duration
}

func newImportHubHomesCmd(newClient kubeclient.Factory, openStore dbflag.Opener) *cobra.Command {
	var f hubImportFlags
	var load wiring.ConfigLoader
	cmd := &cobra.Command{
		Use:   "jupyterhub-homes --from-namespace <namespace>",
		Short: "Copy the home folders of a JupyterHub install (the data science pack) into people's Javo homes",
		Long: "Each claim-<user> claim in --from-namespace is matched to the Javo person whose username or email is " +
			"the JupyterHub username, and its files are copied into a folder of that person's home (--into), " +
			"owned by the person. The JupyterHub claims are only read. People without a match are skipped, or " +
			"created with --create-people from a --subjects file. A home whose JupyterHub server or Javo apps " +
			"run is left for later. Run it again to carry on after a stop or a failure; homes already imported " +
			"are skipped.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			platform, err := load()
			if err != nil {
				return err
			}
			c, err := kubeclient.New(newClient)
			if err != nil {
				return err
			}
			if f.image == "" && !f.dryRun {
				return errors.New("--image is required (the javo image; $JAVO_IMAGE in the server pod)")
			}
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				return runHubImport(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), c, db, platform, f)
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.from, "from-namespace", "", "the namespace JupyterHub keeps its home claims in")
	fl.StringArrayVar(&f.users, "user", nil, "import only this JupyterHub user (repeatable)")
	fl.StringVar(&f.into, "into", "jupyterhub", "the folder of each home the files go to; . is the home itself, where files with the same name are replaced")
	fl.BoolVar(&f.dryRun, "dry-run", false, "say what would be done and change nothing")
	fl.BoolVar(&f.createPeople, "create-people", false, "create the people who have no Javo account yet, from --subjects")
	fl.StringVar(&f.subjects, "subjects", "", "CSV file of username,subject[,email] lines (each user's subject at the identity provider); - for standard input")
	fl.StringVar(&f.image, "image", os.Getenv("JAVO_IMAGE"), "the javo image the copy Jobs run (default $JAVO_IMAGE)")
	fl.IntVar(&f.parallel, "parallel", 4, "how many homes are copied at a time")
	fl.BoolVar(&f.noWait, "no-wait", false, "take one step for each home and return; run again to carry on")
	// With SQLite the controller sees a home this command made only at its
	// next resync (dbsource.DefaultResync, 5 minutes): another process wrote it.
	fl.DurationVar(&f.stuckAfter, "stuck-after", 6*time.Minute, "give up on a home that waits this long for anything but a running copy")
	_ = cmd.MarkFlagRequired("from-namespace")
	load = wiring.ConfigFlag(fl)
	openStore = dbflag.OrRegister(openStore, fl)
	return cmd
}

// hubHome is one JupyterHub home on its way.
type hubHome struct {
	username string
	src      hubimport.Source
	size     resource.Quantity
	person   *model.Person
	target   *hubimport.Target
	started  bool // counted against --parallel
	retried  bool
	final    string // the report line once done, failed, blocked or skipped
	phase    string
	since    time.Time
	followed map[string]bool
}

type hubImport struct {
	out      io.Writer
	mu       *sync.Mutex // out is shared with the log followers
	c        client.Client
	db       *store.Store
	platform config.Platform
	f        hubImportFlags
	subjects map[string]subjectRow
	im       hubimport.Importer
	wg       sync.WaitGroup
}

type subjectRow struct{ subject, email string }

func runHubImport(ctx context.Context, in io.Reader, out io.Writer, c client.Client, db *store.Store, platform config.Platform, f hubImportFlags) error {
	r := &hubImport{out: out, mu: &sync.Mutex{}, c: c, db: db, platform: platform, f: f,
		im: hubimport.Importer{Client: c, Image: podspec.Image{Ref: f.image}, Into: f.into}}
	if f.createPeople {
		if platform.Auth.OIDC == nil || platform.Auth.OIDC.Issuer == "" {
			return errors.New("--create-people needs sign-in through OpenID Connect (auth.oidc): people are made for its issuer")
		}
		if f.subjects == "" {
			return errors.New("--create-people needs --subjects: a person is their subject at the identity provider")
		}
		var err error
		if r.subjects, err = readSubjects(in, f.subjects); err != nil {
			return err
		}
	}
	homes, err := r.sources(ctx)
	if err != nil {
		return err
	}
	if len(homes) == 0 {
		fmt.Fprintf(out, "No JupyterHub home claims (claim-<user>) in namespace %s.\n", f.from)
		return nil
	}
	if f.dryRun {
		for _, h := range homes {
			r.plan(ctx, h)
		}
		r.report(homes, "Dry run: nothing was changed.")
		return nil
	}
	for {
		active := 0
		for _, h := range homes {
			if h.final == "" && h.started {
				active++
			}
		}
		for _, h := range homes {
			if h.final != "" || (!h.started && active >= max(f.parallel, 1)) {
				continue
			}
			wasStarted := h.started
			if err := r.step(ctx, h); err != nil {
				h.final = "failed: " + err.Error()
			}
			if h.started && !wasStarted {
				active++
			}
		}
		left := slices.ContainsFunc(homes, func(h *hubHome) bool { return h.final == "" })
		if !left || f.noWait {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(importPoll):
		}
	}
	r.wg.Wait()
	r.report(homes, "")
	return nil
}

// sources are the home claims in --from-namespace (only --user's when given).
func (r *hubImport) sources(ctx context.Context) ([]*hubHome, error) {
	var l corev1.PersistentVolumeClaimList
	if err := r.c.List(ctx, &l, client.InNamespace(r.f.from)); err != nil {
		return nil, fmt.Errorf("list claims in %s: %w", r.f.from, err)
	}
	var out []*hubHome
	for i := range l.Items {
		pvc := &l.Items[i]
		u, ok := hubimport.Username(pvc)
		if !ok || len(r.f.users) > 0 && !slices.Contains(r.f.users, u) {
			continue
		}
		size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			size = q
		}
		out = append(out, &hubHome{username: u, src: hubimport.Source{Namespace: r.f.from, Claim: pvc.Name}, size: size, followed: map[string]bool{}})
	}
	slices.SortFunc(out, func(a, b *hubHome) int { return strings.Compare(a.username, b.username) })
	return out, nil
}

// readSubjects reads username,subject[,email] lines from path, or from in
// when path is -.
func readSubjects(in io.Reader, path string) (map[string]subjectRow, error) {
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		in = f
	}
	cr := csv.NewReader(in)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	out := map[string]subjectRow{}
	for i, row := range rows {
		if len(row) < 2 || row[0] == "" || row[1] == "" {
			return nil, fmt.Errorf("%s line %d: want username,subject[,email]", path, i+1)
		}
		s := subjectRow{subject: row[1]}
		if len(row) > 2 {
			s.email = row[2]
		}
		out[strings.ToLower(row[0])] = s
	}
	return out, nil
}

// person finds the Javo person of h, or (with --create-people, unless
// dryRun) creates them. A nil person comes with the reason.
func (r *hubImport) person(ctx context.Context, h *hubHome, dryRun bool) (*model.Person, string, error) {
	people := r.db.People(store.IDRange{})
	found, err := people.ByLogin(ctx, h.username)
	if err != nil {
		return nil, "", err
	}
	switch len(found) {
	case 1:
		return &found[0], "", nil
	case 0:
	default:
		names := make([]string, len(found))
		for i, p := range found {
			names[i] = p.Name
		}
		return nil, fmt.Sprintf("skipped: %d people have username or email %q (%s)", len(found), h.username, strings.Join(names, ", ")), nil
	}
	if !r.f.createPeople {
		return nil, fmt.Sprintf("skipped: no Javo person has username or email %q (they have not signed in; or pass --create-people)", h.username), nil
	}
	s, ok := r.subjects[strings.ToLower(h.username)]
	if !ok {
		return nil, fmt.Sprintf("skipped: no Javo person has username or email %q, and %s has no line for them", h.username, r.f.subjects), nil
	}
	issuer := r.platform.Auth.OIDC.Issuer
	switch p, err := people.BySubject(ctx, issuer, s.subject); {
	case err == nil:
		return nil, fmt.Sprintf("skipped: subject %s already belongs to %s, whose username is %q", s.subject, p.Name, p.Username), nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, "", err
	}
	if dryRun {
		return nil, fmt.Sprintf("would create a person for %q (subject %s) and import", h.username, s.subject), nil
	}
	uids := r.platform.Identity.UIDRange
	p, err := r.db.People(store.IDRange{First: uids.First, Last: uids.Last}).Ensure(ctx, store.Claims{
		Issuer: issuer, Subject: s.subject, Username: h.username, Email: s.email,
	})
	if err != nil {
		return nil, "", fmt.Errorf("create person %q: %w", h.username, err)
	}
	r.say(h, "created person %s", p.Name)
	return &p, "", nil
}

func (r *hubImport) namespaceOf(p *model.Person) (string, error) {
	return wiring.Placement(r.platform).NamespaceFor(authz.Principal{ID: p.Name, Groups: p.Groups}, "")
}

// plan says what an import of h would do.
func (r *hubImport) plan(ctx context.Context, h *hubHome) {
	p, why, err := r.person(ctx, h, true)
	switch {
	case err != nil:
		h.final = "failed: " + err.Error()
		return
	case p == nil:
		h.final = why
		return
	}
	h.person = p
	ns, err := r.namespaceOf(p)
	if err != nil {
		h.final = "failed: " + err.Error()
		return
	}
	switch home, err := r.db.Homes().ByOwner(ctx, ns, p.Name); {
	case errors.Is(err, store.ErrNotFound):
		h.final = fmt.Sprintf("would make a home in %s and import into its folder %s", ns, r.f.into)
	case err != nil:
		h.final = "failed: " + err.Error()
	default:
		claim := &corev1.PersistentVolumeClaim{}
		if home.Status.ClaimName != "" && r.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: home.Status.ClaimName}, claim) == nil &&
			claim.Annotations[model.AnnotationImportedFrom] == h.src.String() {
			h.final = "already imported into " + home.Status.ClaimName
			return
		}
		h.final = fmt.Sprintf("would import into %s/%s, folder %s", ns, home.Status.ClaimName, r.f.into)
	}
}

// step takes h one step further.
func (r *hubImport) step(ctx context.Context, h *hubHome) error {
	if h.person == nil {
		p, why, err := r.person(ctx, h, false)
		if err != nil || p == nil {
			h.final = why
			return err
		}
		h.person = p
	}
	if h.target == nil {
		t, waiting, err := r.target(ctx, h)
		if err != nil {
			return err
		}
		if t == nil {
			r.waiting(h, waiting)
			return nil
		}
		h.target, h.started = t, true
	}
	if !h.retried {
		if err := r.im.Retry(ctx, h.src, *h.target); err != nil {
			return err
		}
		h.retried = true
	}
	p, err := r.im.Step(ctx, h.src, *h.target)
	if err != nil {
		return err
	}
	switch p.Phase {
	case hubimport.PhaseDone:
		h.final = fmt.Sprintf("imported into %s/%s, folder %s", h.target.Namespace, h.target.HomeClaim, r.f.into)
	case hubimport.PhaseFailed:
		h.final = "failed: " + strings.Join(p.Detail, "; ")
		if p.Job != nil {
			h.final += fmt.Sprintf(" (kubectl -n %s logs job/%s)", p.Job.Namespace, p.Job.Name)
		}
	case hubimport.PhaseBlocked:
		h.final = "left for later: " + strings.Join(p.Detail, "; ")
	case hubimport.PhaseStaging, hubimport.PhaseCopying:
		r.setPhase(h, string(p.Phase))
		r.follow(ctx, h, p.Job)
	default:
		r.waiting(h, string(p.Phase))
	}
	return nil
}

// target is where h's files go: the person's prepared home. While it is
// being made, waiting says why there is none yet.
func (r *hubImport) target(ctx context.Context, h *hubHome) (*hubimport.Target, string, error) {
	p := h.person
	ns, err := r.namespaceOf(p)
	if err != nil {
		return nil, "", err
	}
	home, err := r.db.Homes().ByOwner(ctx, ns, p.Name)
	if errors.Is(err, store.ErrNotFound) {
		cs, err := r.db.Storage().Cluster(ctx, r.platform.Storage.Spec())
		if err != nil {
			return nil, "", err
		}
		// At least as large as the JupyterHub home, so its files fit.
		size := h.size
		if d := cs.Spec.Home.Size; d != nil && d.Cmp(size) > 0 {
			size = *d
		}
		if home, err = r.db.Homes().Ensure(ctx, ns, p.Name, &size); err != nil {
			return nil, "", fmt.Errorf("make the home of %s: %w", p.Name, err)
		}
		r.say(h, "home of %s asked for (%s)", p.Name, size.String())
	} else if err != nil {
		return nil, "", err
	}
	if !home.Status.Prepared || home.Status.ClaimName == "" {
		why := "waiting for the home to be made"
		for _, c := range home.Status.Conditions {
			if c.Type == model.ConditionReady && c.Message != "" {
				why += ": " + c.Message
			}
		}
		return nil, why, nil
	}
	uid, gid, err := ids.People{People: r.db.People(store.IDRange{}), Mode: r.platform.Identity.UIDMode}.UID(ctx, p.Name)
	if errors.Is(err, ids.ErrNoUID) {
		return nil, "waiting for the person's uid", nil
	} else if err != nil {
		return nil, "", err
	}
	return &hubimport.Target{Owner: p.Name, UID: uid, GID: gid, Namespace: ns, HomeClaim: home.Status.ClaimName}, "", nil
}

// waiting records a wait that is not a running copy, and gives up on it
// after --stuck-after.
func (r *hubImport) waiting(h *hubHome, why string) {
	r.setPhase(h, why)
	if r.f.stuckAfter > 0 && time.Since(h.since) > r.f.stuckAfter {
		h.final = fmt.Sprintf("stuck for %s: %s (run again to carry on)", r.f.stuckAfter, why)
	}
}

func (r *hubImport) setPhase(h *hubHome, phase string) {
	if h.phase != phase {
		h.phase, h.since = phase, time.Now()
		r.say(h, "%s", phase)
	}
}

// follow copies the Job's log into the output, once per Job.
func (r *hubImport) follow(ctx context.Context, h *hubHome, job *types.NamespacedName) {
	if job == nil || FollowJobLog == nil || h.followed[job.String()] {
		return
	}
	h.followed[job.String()] = true
	w := &prefixWriter{mu: r.mu, out: r.out, prefix: h.username + " | "}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if err := FollowJobLog(ctx, *job, w); err != nil {
			r.say(h, "cannot follow the log of %s: %v (kubectl -n %s logs -f job/%s)", job, err, job.Namespace, job.Name)
		}
	}()
}

func (r *hubImport) say(h *hubHome, format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.out, "%s: %s\n", h.username, fmt.Sprintf(format, args...))
}

func (r *hubImport) report(homes []*hubHome, footer string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintln(r.out, "\nJupyterHub home\tJavo person\tresult")
	for _, h := range homes {
		who := "-"
		if h.person != nil {
			who = h.person.Name
		}
		result := h.final
		if result == "" {
			result = "in progress: " + h.phase + " (run again to carry on)"
		}
		fmt.Fprintf(r.out, "%s (%s)\t%s\t%s\n", h.username, h.src.Claim, who, result)
	}
	if footer != "" {
		fmt.Fprintln(r.out, footer)
	}
}

// prefixWriter writes whole lines to out, each after prefix.
type prefixWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	prefix string
	buf    []byte
}

func (w *prefixWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := slices.Index(w.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		w.mu.Lock()
		fmt.Fprintf(w.out, "%s%s\n", w.prefix, w.buf[:i])
		w.mu.Unlock()
		w.buf = w.buf[i+1:]
	}
}

// FollowJobLogInCluster follows the log of the Job's pod through the API
// server, waiting for the pod to start.
func FollowJobLogInCluster(ctx context.Context, job types.NamespacedName, w io.Writer) error {
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	for {
		pods, err := cs.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: batchv1.JobNameLabel + "=" + job.Name})
		if err != nil {
			return err
		}
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodPending {
				continue
			}
			stream, err := cs.CoreV1().Pods(job.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
			if err != nil {
				return err
			}
			defer stream.Close()
			_, err = io.Copy(w, stream)
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(importPoll):
		}
	}
}
