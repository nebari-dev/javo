//go:build !local

package wiring

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/config"
	storagectl "github.com/nebari-dev/javo/internal/controller/storage"
)

// The homes' backups are Jobs of a cluster, which only its controller
// sets up: a binary built with the tag local leaves this file out, and
// with it the storage reconcilers of a cluster.

// HomeBackups is the home folders' backups to the bucket, or nil when the
// install names no bucket.
func HomeBackups(p config.Platform) (*storagectl.HomeBackups, error) {
	c := p.Backup.S3
	if c == nil {
		return nil, nil
	}
	t, err := c.Target()
	if err != nil {
		return nil, err
	}
	hb := &storagectl.HomeBackups{Target: t, Keep: c.Homes.Keep(), Rekey: rekeyer(t)}
	if c.CACertFile != "" {
		if hb.CACert, err = os.ReadFile(c.CACertFile); err != nil {
			return nil, fmt.Errorf("backup.s3.caCertFile: %w", err)
		}
	}
	return hb, nil
}

// rekeyTimeout bounds one move of a repository to its namespace's password:
// restic retries a bucket that does not answer for minutes.
const rekeyTimeout = 2 * time.Minute

// rekeyer moves each repository of the bucket t to its namespace's password
// (backup.Rekey) once per process: a repository done, or made after, stays so.
func rekeyer(t restic.Target) func(ctx context.Context, ns, repo string) error {
	var done sync.Map
	return func(ctx context.Context, ns, repo string) error {
		if _, ok := done.Load(repo); ok {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, rekeyTimeout)
		defer cancel()
		if err := backup.Rekey(ctx, t.Repo(ResticBin(), repo), backup.NamespacePassword(t.Password, ns)); err != nil {
			return err
		}
		done.Store(repo, true)
		return nil
	}
}
