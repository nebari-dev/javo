package kube

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// SecretName is the name of an app's Secret of the given use (api, secrets,
// git, nebi): "<objectName>.app-<use>". Object names are DNS labels and never
// hold a ".", so the part after it says whose Secret it is: no name a person
// picks for an app or a job makes another app's or a job's Secret (jobs use
// ".job-" and ".run-").
func SecretName(objectName, use string) string { return objectName + ".app-" + use }

// Secrets keeps each bundle in a Secret of the app's namespace, labelled
// as the app's, with the bundle's notes as annotations.
type Secrets struct {
	// Reader reads Secrets live: they are never cached.
	Reader client.Reader
	Client client.Client
}

var _ executor.Secrets = Secrets{}

func (s Secrets) Get(ctx context.Context, of workload.Ref, use string) (*executor.Bundle, error) {
	sec, err := OwnedSecret(ctx, s.Reader, of, SecretName(of.Name, use))
	if err != nil || sec == nil {
		return nil, err
	}
	return &executor.Bundle{Data: sec.Data, Meta: sec.Annotations}, nil
}

func (s Secrets) Put(ctx context.Context, of workload.Ref, use string, b executor.Bundle) error {
	name := SecretName(of.Name, use)
	sec, err := OwnedSecret(ctx, s.Reader, of, name)
	if err != nil {
		return err
	}
	create := sec == nil
	if create {
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: of.Namespace}}
	}
	// The app's Service owns the Secret of its secrets: Kubernetes deletes
	// it with the Service, also when the sweep removes an orphaned one. Its
	// values can be made again from the database; the other Secrets hold
	// the only copy of a token.
	if use == executor.UseSecrets {
		svc := &corev1.Service{}
		if err := s.Client.Get(ctx, client.ObjectKey{Namespace: of.Namespace, Name: of.Name}, svc); err != nil {
			return err
		}
		sec.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: svc.Name, UID: svc.UID}}
	}
	sec.Labels = map[string]string{model.LabelApp: of.ID, model.LabelOwner: of.Owner}
	for k, v := range b.Meta {
		metav1.SetMetaDataAnnotation(&sec.ObjectMeta, k, v)
	}
	sec.Type = corev1.SecretTypeOpaque
	sec.Data = b.Data
	if create {
		return s.Client.Create(ctx, sec)
	}
	return s.Client.Update(ctx, sec)
}

func (s Secrets) Delete(ctx context.Context, of workload.Ref, use string) error {
	name := SecretName(of.Name, use)
	sec, err := OwnedSecret(ctx, s.Reader, of, name)
	if errors.Is(err, executor.ErrForeign) {
		log.FromContext(ctx).Info("not deleting a Secret of another owner", "secret", name)
		return nil
	}
	if err != nil || sec == nil {
		return err
	}
	return client.IgnoreNotFound(s.Client.Delete(ctx, sec, client.Preconditions{UID: &sec.UID}))
}

// OwnedSecret reads the Secret name of the app: nil when there is none, an
// error wrapping executor.ErrForeign when it is not labelled with the app's
// id.
func OwnedSecret(ctx context.Context, rd client.Reader, of workload.Ref, name string) (*corev1.Secret, error) {
	sec := &corev1.Secret{}
	err := rd.Get(ctx, client.ObjectKey{Namespace: of.Namespace, Name: name}, sec)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case sec.Labels[model.LabelApp] != of.ID:
		return nil, fmt.Errorf("secret %s/%s %w %s", of.Namespace, name, executor.ErrForeign, of.ID)
	}
	return sec, nil
}
