//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The test binary doubles as javo-sandbox: with JAVO_SANDBOX_AS_MAIN set it
// runs main, so each test drives the real command line in a child process
// (Landlock locks the process that calls it, never the test runner).
func TestMain(m *testing.M) {
	if os.Getenv("JAVO_SANDBOX_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

type result struct {
	code           int
	stdout, stderr string
}

func sandbox(t *testing.T, args ...string) result {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "JAVO_SANDBOX_AS_MAIN=1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running javo-sandbox: %v", err)
	}
	return result{code, out.String(), errOut.String()}
}

// systemRO grants read and execute on the folders a shell needs, as the
// agent pods do; only those that exist on this machine.
func systemRO() []string {
	var args []string
	for _, d := range []string{"/usr", "/bin", "/lib", "/lib64", "/etc"} {
		if _, err := os.Stat(d); err == nil {
			args = append(args, "--ro", d)
		}
	}
	return args
}

func TestLockedCommandWritesItsFolderButCannotReadOutside(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := append([]string{"--rw", project}, systemRO()...)
	args = append(args, "--", "sh", "-c", `echo ok > "$1/out" && cat "$2"`, "sh", project, secret)
	r := sandbox(t, args...)

	if got, err := os.ReadFile(filepath.Join(project, "out")); err != nil || strings.TrimSpace(string(got)) != "ok" {
		t.Errorf("write inside the project: got %q, %v; want ok", got, err)
	}
	if strings.Contains(r.stdout, "s3cret") {
		t.Errorf("the locked command read a file outside its folders: %q", r.stdout)
	}
	if r.code == 0 || !strings.Contains(r.stderr, "Permission denied") {
		t.Errorf("reading outside: exit %d, stderr %q; want a permission error", r.code, r.stderr)
	}
}

// A kernel older than the version asked for fails closed: the command does
// not run at all, rather than run without the lock.
func TestOlderLandlockThanAskedForRefusesToRunTheCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	args := append([]string{"--min-abi", "999", "--rw", filepath.Dir(marker)}, systemRO()...)
	r := sandbox(t, append(args, "--", "touch", marker)...)

	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although the kernel's Landlock is older than asked for")
	}
	if r.code == 0 || !strings.Contains(r.stderr, "older than the 999 needed") || !strings.Contains(r.stderr, "not running it") {
		t.Errorf("exit %d, stderr %q; want a refusal naming the version", r.code, r.stderr)
	}
}

// A folder to grant that does not exist is a mistake in the command line,
// so it fails closed too: the command does not run with less than asked.
func TestMissingFolderToGrantRefusesToRunTheCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	args := append([]string{"--rw", dir, "--ro", filepath.Join(dir, "missing")}, systemRO()...)
	r := sandbox(t, append(args, "--", "touch", marker)...)

	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although a folder to grant is missing")
	}
	if r.code != 125 || !strings.Contains(r.stderr, "missing") || !strings.Contains(r.stderr, "not running the command") {
		t.Errorf("exit %d, stderr %q; want 125 naming the missing folder", r.code, r.stderr)
	}
}

// aHome is a stand-in home with a private key in ~/.ssh and a project in
// ~/projects/api; it returns the home, the project and the key's file.
func aHome(t *testing.T) (home, project, key string) {
	t.Helper()
	home = t.TempDir()
	project = filepath.Join(home, "projects", "api")
	key = filepath.Join(home, ".ssh", "a-private-key")
	for _, d := range []string{project, filepath.Dir(key)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(key, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, project, key
}

// inHome runs javo-sandbox as agent-lock.sh does: no granted path under
// home may be, or pass through, a symbolic link.
func inHome(t *testing.T, home string, args ...string) result {
	t.Helper()
	return sandbox(t, append(append([]string{"--no-links-under", home}, systemRO()...), args...)...)
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// wantLinkRefused fails unless r is the refusal of a link: the command
// never ran, so it printed nothing of the key.
func wantLinkRefused(t *testing.T, r result) {
	t.Helper()
	if strings.Contains(r.stdout, "s3cret") {
		t.Errorf("the locked command read the private key: %q", r.stdout)
	}
	if r.code != 125 || !strings.Contains(r.stderr, "symbolic link") || !strings.Contains(r.stderr, "not running the command") {
		t.Errorf("exit %d, stderr %q; want 125 refusing the symbolic link", r.code, r.stderr)
	}
}

// A folder that is a link to the home would grant the whole home: the lock
// is for the folder the person named, not for where a link leads.
func TestFolderThatIsALinkIsRefused(t *testing.T) {
	home, _, key := aHome(t)
	link := filepath.Join(home, "projects", "everything")
	symlink(t, home, link)
	wantLinkRefused(t, inHome(t, home, "--rw", link, "--", "cat", key))
	wantLinkRefused(t, inHome(t, home, "--rw-make", link, "--", "cat", key))
	wantLinkRefused(t, inHome(t, home, "--ro", link, "--", "cat", key))
}

func TestFolderReachedThroughALinkIsRefused(t *testing.T) {
	home, _, key := aHome(t)
	symlink(t, home, filepath.Join(home, "elsewhere"))
	// ~/elsewhere/.ssh is ~/.ssh by another name.
	wantLinkRefused(t, inHome(t, home, "--rw", filepath.Join(home, "elsewhere", ".ssh"), "--", "cat", key))
}

// A locked agent may make links in its folder. One that leads out of it is
// then not a folder a later app can be locked to, nor made a folder under.
func TestLinkPlantedInAGrantedFolderIsNotANewAppsFolder(t *testing.T) {
	home, project, key := aHome(t)
	sub := filepath.Join(project, "sub")
	if r := inHome(t, home, "--rw", project, "--", "ln", "-s", home, sub); r.code != 0 {
		t.Fatalf("planting the link: exit %d, %s", r.code, r.stderr)
	}
	wantLinkRefused(t, inHome(t, home, "--rw", sub, "--", "cat", key))
	wantLinkRefused(t, inHome(t, home, "--rw-make", filepath.Join(sub, "planted"), "--", "cat", key))
	if _, err := os.Lstat(filepath.Join(home, "planted")); err == nil {
		t.Error("a folder was made through the link, in the home")
	}
}

// An agent's state folder that became a link to ~/.ssh is not granted.
func TestStateFolderReplacedByALinkIsRefused(t *testing.T) {
	home, project, key := aHome(t)
	symlink(t, filepath.Join(home, ".ssh"), filepath.Join(home, ".claude"))
	wantLinkRefused(t, inHome(t, home, "--rw", project, "--rw-make", filepath.Join(home, ".claude"), "--", "cat", key))
	// A file is treated the same.
	symlink(t, key, filepath.Join(home, ".claude.json"))
	wantLinkRefused(t, inHome(t, home, "--rw", project, "--rw", filepath.Join(home, ".claude.json"), "--", "cat", key))
}

// There is no moment between the check and the grant: while the folder is
// swapped with a link to the home over and over, a command is refused or
// locked to the real folder, and never reads the key.
func TestFolderSwappedWithALinkWhileGrantingNeverOpensTheHome(t *testing.T) {
	home, project, key := aHome(t)
	link := filepath.Join(home, "projects", "link")
	symlink(t, home, link)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = unix.Renameat2(unix.AT_FDCWD, project, unix.AT_FDCWD, link, unix.RENAME_EXCHANGE)
			}
		}
	}()
	var refused, locked int
	for range 150 {
		r := inHome(t, home, "--rw", project, "--", "cat", key)
		switch {
		case strings.Contains(r.stdout, "s3cret"):
			t.Fatalf("the locked command read the private key: exit %d, %s", r.code, r.stderr)
		case r.code == 125 && strings.Contains(r.stderr, "symbolic link"):
			refused++
		case strings.Contains(r.stderr, "Permission denied"):
			locked++
		default:
			t.Fatalf("exit %d, stderr %q; want a refusal or a permission error", r.code, r.stderr)
		}
	}
	close(stop)
	<-done
	t.Logf("refused %d times, locked to the real folder %d times", refused, locked)
}

// --rw-make makes the folder, and those above it, when missing.
func TestMakesAMissingFolderToGrant(t *testing.T) {
	home, _, _ := aHome(t)
	fresh := filepath.Join(home, "projects", "new", "deep")
	out := filepath.Join(fresh, "out")
	if r := inHome(t, home, "--rw-make", fresh, "--", "touch", out); r.code != 0 {
		t.Fatalf("exit %d, %s", r.code, r.stderr)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the command did not write its new folder: %v", err)
	}
}

// Only under the folder named are links refused: the home itself may be
// reached through one (/home is a link on some systems), and a path
// outside it is followed as before (/bin is a link to /usr/bin on most).
func TestLinksOutsideTheNamedFolderAreFollowed(t *testing.T) {
	home, project, _ := aHome(t)
	byLink := filepath.Join(t.TempDir(), "home")
	symlink(t, home, byLink)
	system := t.TempDir()
	if err := os.WriteFile(filepath.Join(system, "file"), []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	sysLink := filepath.Join(t.TempDir(), "system")
	symlink(t, system, sysLink)

	r := inHome(t, byLink, "--rw", filepath.Join(byLink, "projects", "api"), "--ro", sysLink,
		"--", "sh", "-c", `cat "$1" && echo ok > "$2"`, "sh", filepath.Join(system, "file"), filepath.Join(project, "out"))
	if r.code != 0 || r.stdout != "readable" {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

// A path that names a parent folder (..) is not the folder it reads as.
func TestPathNamingAParentFolderIsRefused(t *testing.T) {
	home, project, key := aHome(t)
	r := inHome(t, home, "--rw", project+"/../../.ssh", "--", "cat", key)
	if r.code != 125 || !strings.Contains(r.stderr, "..") || strings.Contains(r.stdout, "s3cret") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 125 refusing ..", r.code, r.stdout, r.stderr)
	}
}

// agentLock runs the pod's agent-lock.sh (pod/files/agents) as a pod does:
// from a copy of the pod files with this javo-sandbox beside it in bin/,
// and HOME home.
func agentLock(t *testing.T, home string, args ...string) result {
	t.Helper()
	opt := t.TempDir()
	script, err := os.ReadFile(filepath.Join("..", "..", "pod", "files", "agents", "agent-lock.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"agents", "bin"} {
		if err := os.Mkdir(filepath.Join(opt, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lock := filepath.Join(opt, "agents", "agent-lock.sh")
	if err := os.WriteFile(lock, script, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, os.Args[0], filepath.Join(opt, "bin", "javo-sandbox"))
	cmd := exec.Command(lock, args...)
	cmd.Env = append(os.Environ(), "JAVO_SANDBOX_AS_MAIN=1", "HOME="+home)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if ee, ok := cmd.Run().(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return result{code, out.String(), errOut.String()}
}

// What a person added to what their agent can open is opened as they said:
// a folder to read is read and not written, one to change is written, and
// another project stays shut. One that is a link to ~/.ssh opens nothing:
// the agent does not start at all, so it never reads through the link.
// This is the whole way a pod goes, agent-lock.sh and javo-sandbox, in a
// home outside /tmp (which the script grants).
func TestAgentIsLetIntoAddedFoldersButNeverThroughALink(t *testing.T) {
	home, err := os.MkdirTemp("/var/tmp", "javo-sandbox-home-")
	if err != nil {
		t.Skip("no folder outside /tmp to stand in for a home:", err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	project, data := filepath.Join(home, "projects", "api"), filepath.Join(home, "data")
	key, other := filepath.Join(home, ".ssh", "a-private-key"), filepath.Join(home, "projects", "other", "notes")
	for _, d := range []string{project, data, filepath.Dir(key), filepath.Dir(other)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for file, text := range map[string]string{key: "s3cret", other: "private", filepath.Join(data, "sales.csv"): "q1,10"} {
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const try = `cat "$1/sales.csv"; echo new > "$1/new" && echo written; cat "$2" || true`

	r := agentLock(t, home, project, "--", "sh", "-c", try, "sh", data, other)
	if strings.Contains(r.stdout, "q1,10") || strings.Contains(r.stdout, "written") || strings.Contains(r.stdout, "private") {
		t.Errorf("nothing added: the agent opened %q", r.stdout)
	}
	r = agentLock(t, home, project, "--ro", data, "--", "sh", "-c", try, "sh", data, other)
	if !strings.Contains(r.stdout, "q1,10") || strings.Contains(r.stdout, "written") || strings.Contains(r.stdout, "private") {
		t.Errorf("added to read: stdout %q, stderr %q; want the file read, nothing written and no other project", r.stdout, r.stderr)
	}
	r = agentLock(t, home, project, "--rw", data, "--", "sh", "-c", try, "sh", data, other)
	if !strings.Contains(r.stdout, "q1,10") || !strings.Contains(r.stdout, "written") || strings.Contains(r.stdout, "private") {
		t.Errorf("added to change: stdout %q, stderr %q; want the file read and written, and no other project", r.stdout, r.stderr)
	}

	link := filepath.Join(home, "keys")
	symlink(t, filepath.Dir(key), link)
	for _, mode := range []string{"--ro", "--rw"} {
		wantLinkRefused(t, agentLock(t, home, project, mode, link, "--", "cat", filepath.Join(link, "a-private-key")))
	}
}
