package source

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/reasons"
)

// maxEvidence cuts evidence to what a status condition keeps readable.
const maxEvidence = 600

// Classify reads a failed init container's termination message (for clone,
// the tail of git-sync's log) into a catalog code. ok is false for a
// container that is not one of the four source containers. A source
// container whose text matches nothing known still gets a code, so the
// failure is shown with its text as evidence rather than as something else.
func Classify(container, message string) (reasons.Code, bool) {
	switch container {
	case CloneContainer:
		return classifyClone(message), true
	case CheckContainer:
		if strings.Contains(message, ErrFileMissing.Error()+": ") {
			return reasons.FileNotInRepo, true
		}
		return reasons.CloneFailed, true
	case BuildContainer:
		return reasons.BuildFailed, true
	case SiteCopyContainer:
		if strings.Contains(message, "No such file") {
			return reasons.BuildOutputMissing, true
		}
		return reasons.BuildFailed, true
	}
	return "", false
}

// classifyClone matches what git prints through git-sync. GitHub answers a
// private repository, with or without a token that cannot see it, like a
// missing one ("Repository not found"), so that is RepoNotFound. A server that
// asks for credentials the app has none for reads the same way.
func classifyClone(message string) reasons.Code {
	m := failingText(CloneContainer, message)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(m, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("Repository not found", "returned error: 404", "could not read Username", "terminal prompts disabled"):
		return reasons.RepoNotFound
	case has("Invalid username or token", "Authentication failed", "returned error: 401", "returned error: 403", "Permission denied"):
		return reasons.AuthFailed
	case has("couldn't find remote ref", "not our ref", "invalid refspec"):
		return reasons.RefNotFound
	}
	return reasons.CloneFailed
}

// MaxBuildEvidence is how many lines of a failed build's output its evidence
// keeps: with the container's own line, within what a status condition keeps.
const MaxBuildEvidence = 18

// Evidence is the lines to show for a failed source container. For the build
// it is the build's error lines (buildErrorLines); for clone the last error
// git-sync logged (its "error" field), never the whole log, whose other lines
// carry flags and paths; otherwise the last non-empty line. Each line is cut
// to maxEvidence characters.
func Evidence(container, message string) []string {
	lines := []string{failingText(container, message)}
	if container == BuildContainer {
		lines = buildErrorLines(message)
	}
	for i, l := range lines {
		if r := []rune(l); len(r) > maxEvidence {
			lines[i] = string(r[:maxEvidence]) + "..."
		}
	}
	return lines
}

// errorLine marks the line a build's failure starts at.
var errorLine = regexp.MustCompile(`(?i)\b(error|err!|failed|fatal)\b`)

// buildErrorLines is a failed build's output (the kubelet keeps its last 80
// lines) from the first error line on, the cause usually first, else its last
// lines. Blank lines, npm's pointer to a log file nobody can open and stack
// frames are left out, so the lines that say what went wrong fit.
func buildErrorLines(message string) []string {
	var kept []string
	for _, l := range strings.Split(message, "\n") {
		l = strings.TrimRight(l, " \t\r")
		bare := strings.TrimSpace(strings.TrimPrefix(l, "npm error"))
		if bare == "" || strings.Contains(l, "A complete log of this run can be found in") || strings.HasPrefix(strings.TrimSpace(l), "at ") {
			continue
		}
		kept = append(kept, l)
	}
	if i := slices.IndexFunc(kept, errorLine.MatchString); i >= 0 {
		kept = kept[i:]
		return kept[:min(len(kept), MaxBuildEvidence)]
	}
	return kept[max(0, len(kept)-MaxBuildEvidence):]
}

// failingText is the uncut text Evidence shows. Matching runs on it, not on
// the cut evidence, because a long URL or ref pushes git's reason past the cut.
func failingText(container, message string) string {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	out := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if container != CloneContainer {
			out = line
			break
		}
		var entry struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Error != "" {
			out = entry.Error
			break
		}
	}
	if out == "" && container == CloneContainer {
		// No structured error line: the last plain line is all there is.
		for i := len(lines) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "{") {
				out = l
				break
			}
		}
	}
	return out
}
