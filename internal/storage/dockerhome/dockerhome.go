// Package dockerhome makes a person's home a named volume of the Docker
// daemon their apps run on (the Docker executor).
//
// A home is the volume "javo-<namespace>.home-<owner>", named with the
// same helpers a home's claim is on Kubernetes (naming.HomeClaimName) and
// a workload's network is on Docker, so two people, or one person in two
// namespaces, never share one. Its labels say it is Javo's, which
// namespace and person it is for, and that it is a home. A volume of that
// name with any other labels is someone else's: it is refused, never
// written to and never relabelled.
//
// Nothing here removes a volume. A home outlives the workloads that mount
// it, the person's row and Javo itself, as a retained claim does on
// Kubernetes; a person who comes back finds it by its name and labels.
//
// There is one volume per person and none per group: group folders are
// not offered on Docker yet. A volume of Docker's local driver has no
// size, so a home's size is never enforced.
package dockerhome

import (
	"context"
	"errors"
	"fmt"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/workload"
)

// LabelStorage says what a volume that outlives every workload holds;
// Home is its value for a person's home.
const (
	LabelStorage = "javo.storage"
	Home         = "home"
)

// rootMode is the mode of a home's root: the person's alone.
const rootMode = 0o700

// ErrForeign means the volume that has a home's name is not labelled as
// that person's home. It is left as it is.
var ErrForeign = errors.New("is not this person's home")

// Volumes are the homes kept on one Docker daemon.
type Volumes struct {
	// Client talks to the daemon. Required.
	Client *docker.Client
	// Image is one of the platform's own images (the runner image). A
	// volume is handed to its owner through a container made from it and
	// never started: nothing of the image runs. Required.
	Image string
}

// Name is the name of the volume that is owner's home in namespace.
func Name(namespace, owner string) (string, error) {
	if err := (&model.Home{Spec: model.HomeSpec{Owner: owner}}).Validate(); err != nil {
		return "", fmt.Errorf("dockerhome: %w", err)
	}
	name, err := docker.StorageName(namespace, naming.HomeClaimName(owner))
	if err != nil {
		return "", fmt.Errorf("dockerhome: %w", err)
	}
	return name, nil
}

// labels are what marks a volume as owner's home in namespace.
func labels(namespace, owner string) map[string]string {
	return map[string]string{
		docker.LabelManaged: "true", docker.LabelNamespace: namespace, docker.LabelOwner: owner, LabelStorage: Home,
	}
}

// theirs reports whether have marks a volume as owner's home in
// namespace and as nothing else: not as part of a workload, which would
// end with it.
func theirs(have map[string]string, namespace, owner string) bool {
	for k, want := range labels(namespace, owner) {
		if have[k] != want {
			return false
		}
	}
	return have[docker.LabelEnds] == "" && have[docker.LabelName] == ""
}

// Ensure returns the name of the volume that is owner's home in
// namespace, making it when there is none; fresh says it was made now,
// and so still belongs to root (Own). A volume of that name that is not
// labelled as this person's home is ErrForeign.
func (v Volumes) Ensure(ctx context.Context, namespace, owner string) (name string, fresh bool, err error) {
	if name, err = Name(namespace, owner); err != nil {
		return "", false, err
	}
	found, mine, err := v.Owned(ctx, namespace, owner, name)
	switch {
	case err != nil:
		return "", false, err
	case found && !mine:
		return "", false, foreign(name)
	case found:
		return name, false, nil
	}
	if err := v.Client.VolumeCreate(ctx, name, labels(namespace, owner)); err != nil {
		return "", false, fmt.Errorf("dockerhome: %w", err)
	}
	// The daemon answers a create with the volume of that name, whoever
	// made it: one made in between by someone else is not this home.
	if _, mine, err := v.Owned(ctx, namespace, owner, name); err != nil {
		return "", false, err
	} else if !mine {
		return "", false, foreign(name)
	}
	return name, true, nil
}

func foreign(name string) error { return fmt.Errorf("dockerhome: volume %s %w", name, ErrForeign) }

// Owned reports whether the volume called name exists (found) and is
// labelled as owner's home in namespace (theirs). The gateway asks before
// it lets anything mount the volume a home's row names.
func (v Volumes) Owned(ctx context.Context, namespace, owner, name string) (found, mine bool, err error) {
	have, err := v.Client.VolumeInspect(ctx, name)
	if errors.Is(err, docker.ErrNotFound) {
		return false, false, nil
	} else if err != nil {
		return false, false, fmt.Errorf("dockerhome: %w", err)
	}
	return true, theirs(have.Labels, namespace, owner), nil
}

// Own hands the root of owner's home to uid and gid, closed to everyone
// else. Nothing runs for it, as root or otherwise (docker.Own). The
// volume must exist and be this person's home.
func (v Volumes) Own(ctx context.Context, namespace, owner string, uid, gid int64) error {
	name, err := Name(namespace, owner)
	if err != nil {
		return err
	}
	if found, mine, err := v.Owned(ctx, namespace, owner, name); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("dockerhome: volume %s: %w", name, docker.ErrNotFound)
	} else if !mine {
		return foreign(name)
	}
	if err := docker.EnsureImage(ctx, v.Client, v.Image, false, nil); err != nil {
		return fmt.Errorf("dockerhome: %w", err)
	}
	// The container the volume is handed over through is named after a
	// workload. No app has this name: an app's is "<person>-<app>", and
	// neither a person's name ends nor an app's begins with a dash.
	s := workload.Spec{
		Ref:  workload.Ref{Namespace: namespace, Name: "home--" + owner, Owner: owner},
		User: workload.User{UID: uid, GID: gid},
	}
	if err := docker.Own(ctx, v.Client, s, v.Image, name, rootMode); err != nil {
		return fmt.Errorf("dockerhome: %w", err)
	}
	return nil
}
