package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sigs.k8s.io/yaml"

	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/status"
)

const (
	maxTerminationBytes = 4096
	logTailBytes        = 8 << 10
)

// result is result.json in the run folder.
type result struct {
	Outcome    string    `json:"outcome"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Outputs    []string  `json:"outputs"`
}

// typedParams turns the text values into typed YAML values. A value that does
// not parse as its type names the parameter in the error.
func typedParams(ps []Param) (map[string]any, error) {
	out := make(map[string]any, len(ps))
	for _, p := range ps {
		var v any
		var err error
		switch p.Type {
		case "integer":
			v, err = strconv.ParseInt(p.Value, 10, 64)
		case "number":
			v, err = strconv.ParseFloat(p.Value, 64)
		case "boolean":
			v, err = strconv.ParseBool(p.Value)
		case "string", "":
			v = p.Value
		default:
			err = fmt.Errorf("unknown type %q", p.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("parameter %q is not a valid %s: %q", p.Name, p.Type, p.Value)
		}
		out[p.Name] = v
	}
	return out, nil
}

func writeParams(path string, vals map[string]any) error {
	b, err := yaml.Marshal(vals)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// CopyFile copies src to dst, creating dst with mode perm.
func CopyFile(src, dst string, perm os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(perm)
}

// isPathElement reports whether s is one plain folder name: not empty, not
// "." or "..", no separator. Prune deletes siblings of the run folder, so a
// name that escapes jobs/<definition>/ would delete other folders.
func isPathElement(s string) bool {
	return s != "" && filepath.IsLocal(s) && !strings.ContainsAny(s, `/\`) && s != "."
}

// Fail writes the termination message for a failure that happens before a run
// folder exists and returns the exit code for reason.
func Fail(terminationLog string, reason reasons.Code, message string) int {
	writeTermination(terminationLog, jobstatus.Termination{Reason: string(reason), Message: message})
	return exitFor(string(reason))
}

// writeTermination writes the termination message. If that fails, the error
// goes to stderr so the pod log still shows why the run ended.
func writeTermination(path string, t jobstatus.Termination) {
	if err := os.WriteFile(path, terminationMessage(t), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "javo runner: cannot write the termination message (%s: %s): %v\n", t.Reason, t.Message, err)
	}
}

// prune removes the oldest sibling run folders of cur beyond keep, by
// modification time. The current run is never removed. keep <= 0 keeps all.
func prune(defDir, cur string, keep int) {
	if keep <= 0 {
		return
	}
	es, err := os.ReadDir(defDir)
	if err != nil {
		return
	}
	type dir struct {
		name string
		mod  time.Time
	}
	var olds []dir
	for _, e := range es {
		if !e.IsDir() || e.Name() == cur {
			continue
		}
		if info, err := e.Info(); err == nil {
			olds = append(olds, dir{e.Name(), info.ModTime()})
		}
	}
	slices.SortFunc(olds, func(a, b dir) int { return b.mod.Compare(a.mod) })
	for _, d := range olds[min(keep, len(olds)):] {
		os.RemoveAll(filepath.Join(defDir, d.name))
	}
}

// terminationMessage is the one-line JSON Kubernetes shows as the container's
// termination message, cut so it never exceeds 4096 bytes.
func terminationMessage(t jobstatus.Termination) []byte {
	message := t.Message
	enc := func(msg string) []byte {
		t.Message = msg
		b, _ := json.Marshal(t)
		return b
	}
	b := enc(message)
	for budget := len(message); len(b) > maxTerminationBytes && budget > 0; {
		// Escaping can make the JSON longer than the text, so shrink by the overshoot.
		budget -= len(b) - maxTerminationBytes
		b = enc(status.CapMessage([]string{message}, 1, max(budget, 4)))
	}
	return b
}

// tail keeps the last logTailBytes bytes written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > logTailBytes {
		t.b = t.b[len(t.b)-logTailBytes:]
	}
	return len(p), nil
}

// softWriter forwards to w and remembers the first error without returning it,
// so a full disk does not kill the engine mid-run; the runner reports it after.
type softWriter struct {
	w   io.Writer
	err error
}

func (s *softWriter) Write(p []byte) (int, error) {
	if s.err == nil {
		if _, err := s.w.Write(p); err != nil {
			s.err = err
		}
	}
	return len(p), nil
}

func isNoSpace(err error) bool { return errors.Is(err, syscall.ENOSPC) }

func jsonIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
