// Package hubimport brings the home folders of a JupyterHub install (the
// Nebari data science pack: one claim, claim-<escaped username>, per user,
// owned by uid 1000) into people's Javo homes. A claim can be mounted only
// in its own namespace, so each home moves in three steps, and the
// JupyterHub claim is only ever read:
//
//  1. Stage: a Job in JupyterHub's namespace copies the claim (read-only)
//     to a new staging claim, giving every file the person's uid and gid.
//  2. Move: the staging volume is kept (reclaim policy Retain), its claim is
//     deleted, and the volume is bound to a new claim in Javo's namespace.
//  3. Copy: a Job in Javo's namespace, running as the person, copies the
//     staged files into a folder of their home and checks the copy.
//
// The home claim is then annotated with where it was imported from, and the
// staging volume is deleted. Every step looks at what is already there
// first, so an interrupted import carries on where it stopped.
package hubimport

import (
	"encoding/hex"
	"errors"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// claimPrefix starts every JupyterHub home claim (pvc_name_template
	// claim-{username}).
	claimPrefix = "claim-"
	// annotationUsername is where KubeSpawner keeps the unescaped username.
	annotationUsername = "hub.jupyter.org/username"
)

// Username is the JupyterHub username whose home c is: KubeSpawner's
// annotation, else the claim name with its escaping undone. ok is false for
// a claim that is not a home.
func Username(c *corev1.PersistentVolumeClaim) (string, bool) {
	slug, ok := strings.CutPrefix(c.Name, claimPrefix)
	if !ok || slug == "" {
		return "", false
	}
	if u := c.Annotations[annotationUsername]; u != "" {
		return u, true
	}
	u, err := unescape(slug)
	return u, err == nil
}

// unescape undoes KubeSpawner's "escape" slug scheme: every byte that is not
// a lowercase letter or digit became '-' and two hex digits.
func unescape(slug string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(slug); i++ {
		if slug[i] != '-' {
			b.WriteByte(slug[i])
			continue
		}
		if i+3 > len(slug) {
			return "", errors.New("escape at the end of " + slug)
		}
		v, err := hex.DecodeString(slug[i+1 : i+3])
		if err != nil {
			return "", err
		}
		b.Write(v)
		i += 2
	}
	return b.String(), nil
}
