package redact

import (
	"strings"
	"testing"
)

// Fake credentials in the shapes the scanner knows; none of them is real.
const (
	fakeAWSKey      = "AKIAZ7QW3RTY5UXO6PLK"
	fakeGitHubToken = "ghp_Zq8XbT3kLm9PvR2sW7yN4cJ6hF1dG5aE0uIo"
)

func scanner(t *testing.T) *Scanner {
	t.Helper()
	s, err := NewScanner()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSecretsInALogLineAreHidden(t *testing.T) {
	s := scanner(t)
	for _, line := range []string{
		"boto3 using aws_access_key_id=" + fakeAWSKey,
		"cloning with token " + fakeGitHubToken + " from the env",
	} {
		out, n := s.Redact(line)
		if n == 0 || strings.Contains(out, fakeAWSKey) || strings.Contains(out, fakeGitHubToken) {
			t.Errorf("%q became %q (%d hidden)", line, out, n)
		}
		if !strings.Contains(out, Hidden) {
			t.Errorf("%q: no %s marker in %q", line, Hidden, out)
		}
	}
}

func TestPlainLinesAreUnchanged(t *testing.T) {
	s := scanner(t)
	line := "ModuleNotFoundError: No module named 'plotly'"
	if out, n := s.Redact(line); out != line || n != 0 {
		t.Fatalf("%q became %q (%d hidden)", line, out, n)
	}
}

func TestTwoSecretsInOneLineAreCountedAndHidden(t *testing.T) {
	out, n := scanner(t).Redact("key " + fakeAWSKey + " token " + fakeGitHubToken)
	if n < 2 || strings.Contains(out, fakeAWSKey) || strings.Contains(out, fakeGitHubToken) {
		t.Fatalf("got %q (%d hidden)", out, n)
	}
}
