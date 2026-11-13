//go:build local

package servercmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/nebari-dev/javo/internal/bundle"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/features/authlocal"
	"github.com/nebari-dev/javo/internal/local"
)

// localPort is where `javo local` listens unless told another: a fixed
// port, so the address, and with it the browser's session, is the same
// after every start.
const localPort = 8686

// localRunner is the name the platform's own image has where there are
// no images: a step planned to run /javo of that image runs this binary
// (internal/executor/process). Nothing is ever pulled under it.
const localRunner = "javo"

// NewLocal is `javo local`: `javo server` with executor process, for one
// person on their own machine, with nothing to prepare. It is a command
// of its own because none of the server's flags, its config file or its
// folders are asked for here: internal/local decides them, and the web
// UI, the templates and the platform's files come out of the binary
// (internal/bundle). What `javo server` refuses with executor process it
// refuses here by the same code: root, a second person, unsafe keys or
// folders; it listens on 127.0.0.1 only and has no OIDC.
func NewLocal() *cobra.Command {
	var port int
	var dir string
	cmd := &cobra.Command{
		Use:   "local",
		Short: localShort,
		Long: localShort + ".\n\n" +
			"Apps and jobs run as processes of this machine, as you: nothing is isolated from your account. " +
			"Javo listens on 127.0.0.1 only and prints a link that signs you in. Its database, keys, " +
			"your home folder and the apps' environments are kept in one folder only you can open. " +
			"Environments are installed by pixi, found beside this binary or on PATH; without it the UI works " +
			"and an app that needs an environment says so.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLocal(cmd.Context(), cmd.OutOrStdout(), port, dir)
		},
	}
	cmd.Flags().IntVar(&port, "port", localPort, "port of 127.0.0.1 to listen on; 0 picks a free one")
	cmd.Flags().StringVar(&dir, "dir", "", "folder everything is kept in (default $XDG_DATA_HOME/javo, or ~/.local/share/javo)")
	return cmd
}

// runLocal is the only place that decides what `javo local` runs with,
// and writes what the person needs to know to out.
func runLocal(ctx context.Context, out io.Writer, port int, dir string) error {
	files, err := bundle.Open()
	if err != nil {
		return err
	}
	if dir == "" {
		if dir, err = local.DefaultRoot(); err != nil {
			return err
		}
	}
	dirs, err := local.Prepare(dir)
	if err != nil {
		return err
	}
	if dirs.Files, err = files.Unpack(dirs.Files); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	pixi := local.FindPixi(self)
	hasPixi := pixi != ""
	if !hasPixi {
		if pixi, err = local.WithoutPixi(dirs.Root); err != nil {
			return err
		}
	}
	platform, err := dirs.Settings(pixi)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("%w (is Javo running already? name another port with --port)", err)
	}
	defer listener.Close()
	addr := listener.Addr().String()

	var s serverOptions
	flags := pflag.NewFlagSet("local", pflag.ContinueOnError)
	// Every setting at its default, as a `javo server` given no flag.
	s.register(flags)
	s.common.loadConfig = func() (config.Platform, error) { return platform, nil }
	s.common.database.Path = dirs.Database
	s.gw.listen, s.gw.uiDir, s.ctl.runnerImage = addr, "", localRunner
	db, err := s.common.openDB(ctx, platform)
	if err != nil {
		return err
	}
	err = local.EnsureAccount(ctx, db)
	_ = db.Close()
	if err != nil {
		return err
	}
	// The link is written to the terminal of the person who started
	// Javo, who is the one it signs in, and works once. It is a secret
	// until it is used, so it goes to out and never to the log.
	link := authlocal.NewLink(local.Username, func(link string) {
		fmt.Fprintf(out, "\nSign in with this link (it works once):\n\n    http://%s%s\n\n", addr, link)
	})
	s.gw.carried = &carried{
		listener: listener, ui: files.UI(), templates: files.Templates(), link: link,
		ready: func(h http.Handler) http.Handler {
			fmt.Fprintf(out, "Javo is at http://%s and keeps its files in %s\n", addr, dirs.Root)
			if !hasPixi {
				fmt.Fprintln(out, "pixi was not found beside javo or on PATH: apps and jobs that need an environment will not start until it is installed.")
			}
			link.Announce()
			fmt.Fprintf(out, "For another link, open http://%s%s and look here again. Ctrl-C stops Javo and its apps.\n", addr, authlocal.LinkPath)
			return local.Guard(addr, h)
		},
	}
	return s.run(ctx, flags)
}
