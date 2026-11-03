package model

import (
	"crypto/sha256"
	"encoding/hex"
)

// Environment is the pixi environment an app or a job runs in, picked from
// an outside environment manager and copied in at that moment: its files
// (EnvironmentFiles) are stored beside the app or job, so a run never asks
// the manager. Changing it means picking again.
type Environment struct {
	// Source names the manager it came from, such as "nebi".
	Source string `json:"source"`
	// ID and Name are the manager's id and name of the environment.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Version is the manager's label of the version that was copied.
	Version string `json:"version,omitempty"`
	// Digest is EnvironmentFiles.Digest of the stored files.
	Digest string `json:"digest"`
}

// EnvironmentFiles are an environment's pixi.toml (Manifest) and pixi.lock.
type EnvironmentFiles struct {
	Manifest string
	Lock     string
}

// Digest identifies the files: the hex SHA-256 of both, each preceded by
// its length so no two pairs share one.
func (f EnvironmentFiles) Digest() string {
	h := sha256.New()
	for _, s := range []string{f.Manifest, f.Lock} {
		var n [8]byte
		for i, l := 0, uint64(len(s)); i < 8; i++ {
			n[i] = byte(l >> (8 * i))
		}
		h.Write(n[:])
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (e *Environment) validate(v *validator, field string) {
	if e == nil {
		return
	}
	if e.Source == "" || e.ID == "" || e.Digest == "" {
		v.add(field, "source, id and digest are required")
	}
	v.maxLen(field+".source", e.Source, 63)
	v.maxLen(field+".id", e.ID, 255)
	v.maxLen(field+".name", e.Name, 255)
	v.maxLen(field+".version", e.Version, 63)
	v.maxLen(field+".digest", e.Digest, 64)
}
