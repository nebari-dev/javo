package backupcmd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/backup"
)

// followStep is one part of a full backup or restore as the API reports it.
type followStep struct {
	Kind     string  `json:"kind"`
	Name     string  `json:"name"`
	Person   string  `json:"person"`
	Account  bool    `json:"account"`
	Claim    string  `json:"claim"`
	Node     string  `json:"node"`
	Dest     string  `json:"dest"`
	Files    int     `json:"files"`
	Bytes    int64   `json:"bytes"`
	Uploaded int64   `json:"uploaded"`
	Took     float64 `json:"took"`
	State    string  `json:"state"`
	Status   struct {
		Title string `json:"title"`
	} `json:"status"`
	Evidence []string `json:"evidence"`
	Log      []string `json:"log"`
}

type followProgress struct {
	Steps  []followStep `json:"steps"`
	Done   bool         `json:"done"`
	Failed int          `json:"failed"`
}

// apiClient calls the javo API as a job's run does: JAVO_URL with the owner's
// token JAVO_TOKEN (a job from the backup template gets both).
type apiClient struct {
	base, host, token string
	http              *http.Client
	// retryFor is how long a GET is tried again while the gateway does not
	// answer (it restarts), zero being two minutes; retryEvery how often,
	// zero being 3s.
	retryFor, retryEvery time.Duration
}

// apiFromEnv reads JAVO_URL and JAVO_TOKEN. With JAVO_API_URL (the gateway
// inside the cluster) the calls go there, carrying JAVO_URL's host.
func apiFromEnv() (apiClient, error) {
	public, tok := os.Getenv("JAVO_URL"), os.Getenv("JAVO_TOKEN")
	if public == "" || tok == "" {
		return apiClient{}, errors.New("JAVO_URL and JAVO_TOKEN are not set: run this as a job from the backup template")
	}
	c := apiClient{base: strings.TrimRight(public, "/"), token: tok, http: &http.Client{Timeout: 2 * time.Minute}}
	if internal := os.Getenv("JAVO_API_URL"); internal != "" {
		u, err := url.Parse(public)
		if err != nil {
			return c, fmt.Errorf("JAVO_URL: %w", err)
		}
		c.base, c.host = strings.TrimRight(internal, "/"), u.Host
	}
	return c, nil
}

func (c apiClient) do(ctx context.Context, method, path string, body any, out any) error {
	if method != http.MethodGet {
		return c.once(ctx, method, path, body, out)
	}
	retryFor, every := cmp.Or(c.retryFor, 2*time.Minute), cmp.Or(c.retryEvery, 3*time.Second)
	deadline := time.Now().Add(retryFor)
	for {
		err := c.once(ctx, method, path, body, out)
		var u unavailable
		if err == nil || (!errors.As(err, &u) && !isTransport(err)) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(every):
		}
	}
}

// unavailable is the gateway answering that it cannot serve now.
type unavailable struct{ status string }

func (u unavailable) Error() string { return u.status }

// isTransport is an error before any answer: the gateway is not there.
func isTransport(err error) bool {
	var ue *url.Error
	return errors.As(err, &ue)
}

func (c apiClient) once(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.host != "" {
		req.Host = c.host
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%s %s: %w", method, path, unavailable{resp.Status})
	}
	if resp.StatusCode/100 != 2 {
		var p struct{ Title, Detail string }
		if json.Unmarshal(data, &p) == nil && p.Title != "" {
			return fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(p.Title+" "+p.Detail))
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return json.Unmarshal(data, out)
}

// follow prints an event line whenever a step's state changes and each new
// line of the step's own log as it comes, until every step ended; then a
// summary table. It returns an error when a step failed, so the job's run
// fails.
func follow(ctx context.Context, out io.Writer, first followProgress, next func() (followProgress, error), poll time.Duration) error {
	state := map[string]string{}
	printed := map[string]map[string]bool{}
	p := first
	for {
		for _, s := range p.Steps {
			key := s.Kind + "\x00" + s.Name
			if s.Kind == "database" || s.Kind == "apps" {
				// Named by its file once done: one step all the same.
				key = s.Kind
			}
			if state[key] != s.State {
				state[key] = s.State
				kv := []any{"status", s.Status.Title}
				if len(s.Evidence) > 0 {
					kv = append(kv, "evidence", strings.Join(s.Evidence, "; "))
				}
				stepLog(s).Event(strings.ToLower(s.State), kv...)
			}
			if printed[key] == nil {
				printed[key] = map[string]bool{}
			}
			for _, l := range s.Log {
				if !printed[key][l] {
					printed[key][l] = true
					fmt.Fprintln(out, l)
				}
			}
		}
		if p.Done {
			return summarize(out, p)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		var err error
		if p, err = next(); err != nil {
			return err
		}
	}
}

// stepLog is an event log whose lines name the step, writing to stdout.
func stepLog(s followStep) *backup.EventLog {
	fields := []string{"step", s.Kind, "name", s.Name}
	for _, f := range [][2]string{{"person", s.Person}, {"claim", s.Claim}, {"node", s.Node}} {
		if f[1] != "" {
			fields = append(fields, f[0], f[1])
		}
	}
	return &backup.EventLog{W: stepOut, Fields: fields}
}

// stepOut is where follow writes; the command sets it to its output.
var stepOut io.Writer = os.Stdout

// summarize prints the summary table and what the run held, and fails
// when a step failed.
func summarize(out io.Writer, p followProgress) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "SUMMARY")
	fmt.Fprintln(tw, "STEP\tNAME\tWHO\tSTATE\tFILES\tSIZE\tUPLOADED\tTOOK\tWHERE")
	homes, volumes, folders := 0, 0, 0
	for _, s := range p.Steps {
		switch {
		case s.Kind == "home" && s.Account:
			homes++
		case s.Kind == "home":
			volumes++
		case s.Kind == "folder":
			folders++
		}
		files := "-"
		if s.Files > 0 {
			files = strconv.Itoa(s.Files)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Kind, s.Name, dash(s.Person), s.State, files,
			backup.Size(s.Bytes), backup.Size(s.Uploaded), backup.Took(s.Took), dash(s.Dest))
	}
	_ = tw.Flush()
	held := fmt.Sprintf("the database, %s, %s and %s",
		count(homes, "home folder of a person", "home folders of people"),
		count(volumes, "home volume with no account", "home volumes with no account"),
		count(folders, "shared folder", "shared folders"))
	if p.Failed > 0 {
		fmt.Fprintf(out, "%d of %d parts failed: %s.\n", p.Failed, len(p.Steps), held)
		return fmt.Errorf("%d of %d parts failed", p.Failed, len(p.Steps))
	}
	fmt.Fprintf(out, "All %d parts done: %s.\n", len(p.Steps), held)
	return nil
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// newBackupRunCmds are `javo backup run` and `javo backup restore-run`, what
// runs of the backup and restore jobs run. The platform does the work; the
// run starts it for itself (JAVO_RUN_ID) and follows it.
func newBackupRunCmds() []*cobra.Command {
	var poll time.Duration
	run := &cobra.Command{
		Use: "run", Short: "Back up the database, every home folder and every shared folder to the bucket (the backup job runs this)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, id, err := runIdentity(poll)
			if err != nil {
				return err
			}
			var first followProgress
			if err := c.do(cmd.Context(), http.MethodPost, "/api/admin/backups/runs", map[string]string{"run": id}, &first); err != nil {
				return err
			}
			stepOut = cmd.OutOrStdout()
			return follow(cmd.Context(), cmd.OutOrStdout(), first, func() (followProgress, error) {
				var p followProgress
				err := c.do(cmd.Context(), http.MethodGet, "/api/admin/backups/runs/"+url.PathEscape(id)+"?logs=true", nil, &p)
				return p, err
			}, poll)
		},
	}
	restore := &cobra.Command{
		Use: "restore-run", Short: "Restore a full backup for everyone or one person (the restore job runs this; parameters from, person, mode)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, id, err := runIdentity(poll)
			if err != nil {
				return err
			}
			body := map[string]string{"run": id}
			for key, env := range map[string]string{"from": "JAVO_PARAM_FROM", "person": "JAVO_PARAM_PERSON", "mode": "JAVO_PARAM_MODE"} {
				if v := os.Getenv(env); v != "" {
					body[key] = v
				}
			}
			if body["from"] == "" {
				return errors.New("the run has no parameter from: the backup to restore")
			}
			var first followProgress
			if err := c.do(cmd.Context(), http.MethodPost, "/api/admin/backups/restores", body, &first); err != nil {
				return err
			}
			stepOut = cmd.OutOrStdout()
			return follow(cmd.Context(), cmd.OutOrStdout(), first, func() (followProgress, error) {
				var p followProgress
				err := c.do(cmd.Context(), http.MethodGet, "/api/admin/backups/restores/"+url.PathEscape(id)+"?logs=true", nil, &p)
				return p, err
			}, poll)
		},
	}
	for _, c := range []*cobra.Command{run, restore} {
		c.Flags().DurationVar(&poll, "poll", 5*time.Second, "how often to look again")
	}
	return []*cobra.Command{run, restore}
}

func runIdentity(poll time.Duration) (apiClient, string, error) {
	c, err := apiFromEnv()
	if err != nil {
		return c, "", err
	}
	// A gateway that does not answer is asked again as often as the run
	// looks for progress, and no less often than every 3s.
	c.retryEvery = min(poll, 3*time.Second)
	id := os.Getenv("JAVO_RUN_ID")
	if id == "" {
		return c, "", errors.New("JAVO_RUN_ID is not set: run this as a job")
	}
	return c, id, nil
}
