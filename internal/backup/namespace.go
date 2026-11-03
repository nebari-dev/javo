package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/reasons"
)

// NamespacePassword is the password of the repositories of namespace ns
// (its homes and shared folders): an HMAC-SHA256 of the namespace under the
// backup password. It is all a namespace's backup Secret holds, so reading
// that Secret opens only that namespace's repositories, never another's or
// the database's, which keep the backup password in the release namespace.
func NamespacePassword(master, ns string) string {
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte("javo backup namespace v1\x00" + ns))
	return hex.EncodeToString(m.Sum(nil))
}

// NamespaceTarget is t with the password of namespace ns.
func NamespaceTarget(t restic.Target, ns string) restic.Target {
	t.Password = NamespacePassword(t.Password, ns)
	return t
}

func hasCode(err error, code reasons.Code) bool {
	var re *restic.Error
	return errors.As(err, &re) && re.Code == code
}

// Rekey moves the repository, opened by master with the backup password, to
// password (its namespace's): repositories made before namespaces had their
// own password open with the backup password until then. A repository that
// password already opens, or that does not exist yet, is left as it is.
func Rekey(ctx context.Context, master restic.Repo, password string) error {
	switch err := master.WithPassword(password).Opens(ctx); {
	case err == nil, hasCode(err, reasons.BackupRepoMissing):
		return nil
	case !hasCode(err, reasons.BackupWrongPassword):
		return err
	}
	return master.ChangePassword(ctx, password)
}

// ListSnapshots lists the snapshots with tags of the repository master
// opens, with password (its namespace's), or with the backup password while
// it has not been moved to it yet (Rekey).
func ListSnapshots(ctx context.Context, master restic.Repo, password string, tags ...string) ([]restic.Snapshot, error) {
	snaps, err := master.WithPassword(password).Snapshots(ctx, tags...)
	if hasCode(err, reasons.BackupWrongPassword) {
		return master.Snapshots(ctx, tags...)
	}
	return snaps, err
}
