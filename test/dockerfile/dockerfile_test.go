// Package dockerfile checks that every image a Dockerfile in this repository
// makes BuildKit pull is pinned by digest: the frontend named by the syntax
// directive (the parser of the whole build) and every FROM that is not
// scratch or an earlier stage. A tag alone can be moved to other content.
package dockerfile

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	digest  = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
	syntax  = regexp.MustCompile(`(?i)^#\s*syntax\s*=\s*(\S+)`)
	argRef  = regexp.MustCompile(`\$\{?[A-Za-z_][A-Za-z0-9_]*\}?`)
	fromCmd = regexp.MustCompile(`(?i)^FROM\s+`)
)

// problems lists the unpinned images of one Dockerfile.
func problems(src []byte) []string {
	var out []string
	stages := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(src))
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			first = false
			if m := syntax.FindStringSubmatch(line); m != nil && !digest.MatchString(m[1]) {
				out = append(out, "syntax directive "+m[1]+" is not pinned by digest")
			}
		}
		if !fromCmd.MatchString(line) {
			continue
		}
		fields := strings.Fields(line)[1:]
		for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		image := fields[0]
		switch {
		case image == "scratch", stages[strings.ToLower(image)], digest.MatchString(image):
		case argRef.MatchString(image) && earlierStage(stages, image):
		default:
			out = append(out, "FROM "+image+" is not pinned by digest")
		}
		// A stage is usable from the next FROM on.
		for name := range pendingStages(fields) {
			stages[name] = true
		}
	}
	return out
}

func pendingStages(fields []string) map[string]bool {
	m := map[string]bool{}
	if len(fields) >= 3 && strings.EqualFold(fields[1], "AS") {
		m[strings.ToLower(fields[2])] = true
	}
	return m
}

// earlierStage says whether an image with build arguments in it (ttyd-${ARCH})
// can only name earlier stages: the parts around the arguments match one.
func earlierStage(stages map[string]bool, image string) bool {
	if argRef.ReplaceAllString(image, "") == "" {
		return false // only arguments: any image at all
	}
	re := regexp.MustCompile("^" + strings.Join(func() []string {
		var parts []string
		for _, p := range argRef.Split(strings.ToLower(image), -1) {
			parts = append(parts, regexp.QuoteMeta(p))
		}
		return parts
	}(), "[a-z0-9_.-]+") + "$")
	for s := range stages {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func TestEveryImageTheBuildsPullIsPinnedByDigest(t *testing.T) {
	root := filepath.Join("..", "..")
	// CI runs on the commit's tarball, without .git, so walk the tree.
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".pixi") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "Dockerfile") {
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("found only %v; expected the image's and the fake GitHub's Dockerfiles", files)
	}
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems(src) {
			t.Errorf("%s: %s", f, p)
		}
	}
}

func TestTheCheckCatchesUnpinnedImages(t *testing.T) {
	const pin = "@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e"
	cases := map[string]string{
		"# syntax=docker/dockerfile:1\nFROM scratch\n":                   "syntax directive",
		"# syntax=docker/dockerfile:1.27.1\nFROM scratch\n":              "syntax directive",
		"FROM golang:1.26\n":                                             "FROM golang:1.26",
		"FROM --platform=$BUILDPLATFORM node:22 AS ui\n":                 "FROM node:22",
		"FROM base AS x\n":                                               "FROM base",
		"FROM alpine" + pin + " AS a\nFROM b-${ARCH}\n":                  "FROM b-${ARCH}",
		"FROM alpine" + pin + " AS a\nFROM a\nFROM ${IMAGE}\n":           "FROM ${IMAGE}",
		"FROM alpine" + pin + " AS one\nFROM busybox:latest AS two\n":    "FROM busybox:latest",
		"# syntax=docker/dockerfile:1" + pin + "\nFROM debian:13 AS d\n": "FROM debian:13",
	}
	for src, want := range cases {
		got := strings.Join(problems([]byte(src)), "\n")
		if !strings.Contains(got, want) {
			t.Errorf("%q: want a problem with %q, got %q", src, want, got)
		}
	}
	ok := "# syntax=docker/dockerfile:1.27.1" + pin + "\n" +
		"FROM scratch AS t-amd64\nFROM scratch AS t-arm64\nFROM t-${BUILDARCH} AS t\n" +
		"FROM --platform=$BUILDPLATFORM golang:1.26" + pin + " AS build\nFROM build\n"
	if p := problems([]byte(ok)); len(p) > 0 {
		t.Errorf("a pinned Dockerfile is refused: %v", p)
	}
}
