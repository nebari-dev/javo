// Package process runs apps as child processes of Javo, for one person on
// their own machine. It is an executor.Executor built on
// internal/executor/supervise, for Linux and macOS.
//
// # What it does not do
//
// It isolates nothing. Every unit runs as Javo's own user, sees every file
// that user sees (other apps' folders, the home, Javo's own state
// directory with its secret files), may reach every address, and may
// signal every other process of that user. Treat what it runs as you
// treat your own shell. A read-only mount is not read-only, a volume's
// limit is not a limit, and sizes and resources are not applied. Javo
// must refuse a second person and must not serve beyond loopback in this
// mode; this package only refuses to be built as root (uid 0).
//
// What a unit prints is its own: a unit that prints a secret puts it in
// its log file and, when it then fails, in the evidence of its state.
//
// # What a workload.Spec means without containers
//
// Images. There are none. A unit runs when
//
//   - its Image is empty: its command is a program of this machine, found
//     on Javo's PATH; or
//   - its Image is the platform's own (Options.RunnerImage) and its command
//     is /javo: the running Javo binary (os.Executable) takes its place; or
//   - it is the step that writes a passwd and group file for the
//     container's user (podfiles.IdentityContainer): it is left out, with
//     every mount of the volume it writes, because this machine already
//     knows the user.
//
// Any other unit needs its image, and Start refuses the workload with an
// *UnsupportedError that names it. That is the template's to fix: show it
// as reasons.TemplateInvalid.
//
// Volumes. Each is a folder of this machine: a volume with a Source is
// that absolute path (it must exist; nothing is made or changed there), a
// volume of secret values is a folder under the state directory, mode
// 0700, holding one 0600 file per key (WorldReadable is ignored), and any
// other is an empty folder under the state directory that goes with the
// workload. VolumeDir says where.
//
// Mounts. Nothing can be shown at another path, and no path is rewritten:
// a command line is words, and a word cannot be told from a path. So a
// mount is honoured only when its Path is the volume's own folder (plus
// SubPath). Any other mount is refused with an *UnsupportedError, rather
// than run the unit with paths that do not exist or, worse, exist and
// belong to something else. A unit's Dir must lie in one of its mounts;
// an empty Dir is a folder of the workload's own. Whoever plans a
// workload for this executor must plan it with the folders VolumeDir
// names; a plan made for containers (/home/user, /opt/javo, /scratch) is
// refused. Paths inside arguments and variables are passed as they are.
//
// User. Units run as Javo. A Spec that names another uid or gid, or a
// group Javo is not in, is refused.
//
// Port. One loopback port per workload, picked when Addr is first asked
// and written to the state directory, so it is the same after Javo
// restarts; Stop gives it up. Addr answers 127.0.0.1 and that port,
// whatever port it is asked for, and a Spec whose Port is another is
// refused: the app must be told to listen there, on 127.0.0.1. Nothing
// holds the port while the app does not run; an app that finds it taken
// fails as a crashed app.
//
// Environment. A unit gets PATH from Javo and the variables its Spec
// names (a PATH among them replaces Javo's), nothing else of Javo's own
// environment. Values of bundles are
// read from the executor.Secrets given to New each time a unit starts and
// go into the child's environment or the 0600 files above, never into a
// log line of Javo's, an error or any other file.
//
// Runs. A Spec with Batch is refused with ErrBatch: job runs are not
// supported yet.
//
// # The shipped templates
//
// The planner (internal/controller/workspace) plans an app for this
// executor when it is given Host: the folders of this machine in place of
// a container's paths, no image, the port Addr names and Javo's own uid
// and gid. workspace.RunsAsProcesses says which templates can run this
// way on which platform: the ones that name no image and take their
// program from a pixi environment locked for the machine, which the install step (internal/pixienv) installs with
// the machine's sh and the tools of tools.go. A template that needs an
// image (harf, rstudio, static-site, container), an app from a repository
// and an app with Nebi access are refused, and the templates for job runs
// stay out.
//
// What the machine must have: sh and bash, the pixi binary of
// Options.Pixi, the platform's files in Options.Files, and a bundled
// environment locked for the machine's platform (Host names it). The
// bundled environments (pod/files/envs) are locked for Linux and for
// macOS on Apple silicon (osx-arm64), but for the two with Julia, which
// conda-forge does not build for macOS: the planner refuses the pluto
// template on a Mac and says so. None is locked for an Intel Mac.
//
// # Loopback
//
// An app must listen on 127.0.0.1 and nothing else: the planner tells it
// to, where the template or the typed command says so in a word of its
// own, and guard.go kills an app whose port answers on another address of
// this machine. See guard for what that does not catch.
//
// # Processes
//
// Each unit is the leader of a process group of its own. Stop sends the
// group SIGTERM, then SIGKILL after Options.Grace. When the leader exits,
// what is left of its group is killed, as a container ends with its first
// process. A group is signalled only while its leader has not been
// reaped, which is the only time its number is provably ours.
//
// Close stops everything. When Javo dies without closing, Linux kills
// each leader (PR_SET_PDEATHSIG), not the processes a leader started;
// macOS has no such thing and kills nothing. Either way New sweeps: each
// unit's pid is recorded with the time the process started (pids/<unit>
// in the workload's folder), and a process that still has both is killed
// with its group. A recorded pid whose start time differs, or whose
// leader is gone, is somebody else's by now and is left alone, so
// processes a dead leader left behind keep running until they exit.
//
// # Logs
//
// Each unit's output goes to logs/<unit>.log in the workload's folder,
// each line stamped with its time, and the run before it to
// <unit>.previous.log. They are not rotated by size, and Stop removes
// them with the rest of the workload's folder.
package process
