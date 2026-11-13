package workspace

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// What an app with API access (model.APIAccessOf not None) gets: a Secret in
// its namespace, labelled with the app's id and deleted with it, holding its
// token (APITokenKey, read into
// JAVO_TOKEN) and the API guide (APIGuideKey, mounted read-only at
// APIGuidePath and at each of the template's apiGuidePaths).
const (
	APITokenKey  = "token"
	APIGuideKey  = "AGENTS.md"
	APIGuidePath = "/opt/javo/AGENTS.md"

	apiGuideVolume = "javo-api-guide"
	envURL         = "JAVO_URL"
	envInternalURL = "JAVO_API_URL"
	envToken       = workload.EnvToken
)

//go:embed agents_guide.md
var apiGuide []byte

// TokenMinter stores a new API token and returns its text, and reads a
// token's record. The controller never lists tokens.
type TokenMinter interface {
	Get(ctx context.Context, id string) (token.Token, error)
	Create(ctx context.Context, actor model.Actor, t token.Token) (string, token.Token, error)
	Revoke(ctx context.Context, actor model.Actor, owner, id string) (token.Token, error)
}

// APITokenSecretName is the Secret holding the API token of the app whose Pod
// and Service are called objectName (see SecretName).
func APITokenSecretName(objectName string) string {
	return SecretName(objectName, "api")
}

// addAPIToken gives the main unit the owner's API token from the app's
// bundle as JAVO_TOKEN, the UI's public URL as JAVO_URL and the API guide.
// With access None it does nothing. The token is never in the Spec and
// never a file.
func addAPIToken(s *workload.Spec, t *model.Template, in Inputs) {
	if token.ScopeFor(in.APIAccess) == "" {
		return
	}
	app := &s.Main
	if in.APIInternalURL != "" {
		app.Env = append(app.Env, workload.Env{Name: envInternalURL, Value: in.APIInternalURL})
	}
	app.Env = append(app.Env,
		workload.Env{Name: envURL, Value: in.APIURL},
		workload.Env{Name: envToken, From: &workload.SecretRef{Bundle: APITokenSecretName(s.Ref.Name), Key: APITokenKey}},
	)
	for _, p := range append([]string{APIGuidePath}, t.Spec.APIGuidePaths...) {
		app.Mounts = append(app.Mounts, workload.Mount{Volume: apiGuideVolume, Path: p, SubPath: APIGuideKey, ReadOnly: true})
	}
	s.Volumes = append(s.Volumes, workload.Volume{Name: apiGuideVolume, Secret: &workload.SecretFiles{
		Bundle: APITokenSecretName(s.Ref.Name), Keys: []string{APIGuideKey},
	}})
}

// ensureAPIToken makes sure the app has a live token of its owner with scope
// in its Secret and returns the Secret's name. A new token is made when the
// Secret is missing, holds a token of another scope, holds the token the
// gateway marked revoked (App.TokenRevoked), names a token that has no
// record, or names one whose record is not this app's (another app's or a
// job's is never adopted). A Secret of that name labelled for another app is
// refused. The token it replaces is revoked by the gateway, which refuses and
// sweeps app tokens whose scope no longer matches their app. The token
// record is written first: a failure after it leaves a record no one holds,
// which the owner sees in Settings and the janitor removes with the app.
func (r *Reconciler) ensureAPIToken(ctx context.Context, app *model.App, scope token.Scope) (string, error) {
	if r.Tokens == nil || r.Secrets == nil {
		return "", errors.New("the template gives apps an API token, but the controller has no token store")
	}
	name, ref := APITokenSecretName(app.ObjectName()), RefOf(app)
	have, err := r.Secrets.Get(ctx, ref, name)
	if err != nil {
		return "", err
	}
	if have != nil {
		id := have.Meta[model.AnnotationTokenID]
		live := false
		if id != "" && bundleScope(have) == scope && id != app.TokenRevoked && len(have.Data[APITokenKey]) > 0 {
			// A Secret can name a token whose record is gone (an upgrade, or
			// `javo user remove`): its token gets 401, so it is replaced.
			switch tok, err := r.Tokens.Get(ctx, id); {
			case errors.Is(err, token.ErrNotFound):
			case err != nil:
				return "", fmt.Errorf("look up the app's API token: %w", err)
			default:
				live = tokenOfApp(tok, app)
			}
		}
		if live {
			if bytes.Equal(have.Data[APIGuideKey], apiGuide) {
				return name, nil
			}
			// A newer guide ships with this controller; the token stays.
			have.Data[APIGuideKey] = apiGuide
			return name, r.Secrets.Put(ctx, ref, name, *have)
		}
	}
	text, tok, err := r.Tokens.Create(ctx, model.Actor{System: model.SystemController}, token.Token{
		Name: app.Name, Owner: app.Owner, Scope: scope,
		App: &token.App{Namespace: app.Namespace, Name: app.ObjectName(), Short: app.Name},
	})
	if err != nil {
		return "", fmt.Errorf("make the app's API token: %w", err)
	}
	err = r.Secrets.Put(ctx, ref, name, executor.Bundle{
		Data: map[string][]byte{APITokenKey: []byte(text), APIGuideKey: apiGuide},
		Meta: map[string]string{model.AnnotationTokenID: tok.ID, model.AnnotationTokenScope: string(scope)},
	})
	if err != nil {
		// No Secret holds the new token, so it must not keep working; the
		// next reconcile makes another.
		if _, rerr := r.Tokens.Revoke(ctx, model.Actor{System: model.SystemController}, app.Owner, tok.ID); rerr != nil && !errors.Is(rerr, token.ErrNotFound) {
			return "", fmt.Errorf("store the app's API token: %w (and revoke it: %v)", err, rerr)
		}
		return "", fmt.Errorf("store the app's API token: %w", err)
	}
	return name, nil
}

// bundleScope is the scope of the token an app's Secret holds; a Secret made
// before the annotation existed holds a read-write one.
func bundleScope(b *executor.Bundle) token.Scope {
	if s, ok := b.Meta[model.AnnotationTokenScope]; ok {
		return token.Scope(s)
	}
	return token.ScopeReadWrite
}

// removeAPIToken deletes the app's token Secret, if any. Its token record
// is the gateway's to revoke (see ensureAPIToken).
func (r *Reconciler) removeAPIToken(ctx context.Context, app *model.App) error {
	return r.Secrets.Delete(ctx, RefOf(app), APITokenSecretName(app.ObjectName()))
}
