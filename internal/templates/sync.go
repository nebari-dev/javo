// Package templates keeps the built-in templates, the ones that come with the
// installation, in the templates table.
package templates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// file is one template file: the shape the chart renders (and kubectl once
// applied), so one file serves both.
type file struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec model.TemplateSpec `json:"spec"`
}

// Sync makes the built-in rows of the templates table match the *.yaml files
// in files: a file adds or updates its template, a built-in row without a file
// is deleted. A template an administrator saved (source admin) is never
// changed or deleted, also when a file carries its name. Every file is read
// and checked before anything is written, so a bad file changes nothing.
func Sync(ctx context.Context, db *store.Store, files fs.FS) error {
	want, err := load(files)
	if err != nil {
		return err
	}
	actor := model.Actor{System: model.SystemController}
	for _, t := range want {
		if err := upsert(ctx, db, actor, t); err != nil {
			return fmt.Errorf("template %s: %w", t.Name, err)
		}
	}
	have, err := db.Templates().List(ctx)
	if err != nil {
		return err
	}
	for _, old := range have {
		if old.Source != store.SourceBuiltin || slices.ContainsFunc(want, func(t model.Template) bool { return t.Name == old.Name }) {
			continue
		}
		if err := db.Templates().Delete(ctx, actor, old.Name); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("template %s: %w", old.Name, err)
		}
	}
	return nil
}

// upsert writes t unless the stored row already matches. Gateways that start
// together all run Sync, so a create or update can lose to another one
// (ErrExists, ErrConflict): then the row is read again and judged afresh, which
// ends once the winner's write is seen.
func upsert(ctx context.Context, db *store.Store, actor model.Actor, t model.Template) error {
	for range upsertAttempts {
		old, err := db.Templates().Get(ctx, t.Name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			_, err = db.Templates().Put(ctx, actor, t, 0)
		case err != nil:
			return err
		case old.Source != store.SourceBuiltin:
			slog.Warn("built-in template skipped: an administrator's template has its name", "template", t.Name)
			return nil
		case sameSpec(old.Spec, t.Spec):
			return nil
		default:
			_, err = db.Templates().Put(ctx, actor, t, old.Version)
		}
		if !errors.Is(err, store.ErrExists) && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return fmt.Errorf("lost the race to other writers %d times", upsertAttempts)
}

// upsertAttempts bounds upsert's retries; each lost round means another writer made progress.
const upsertAttempts = 10

// Parse reads one built-in template file the way Sync does: strict decoding,
// defaults applied, then validated.
func Parse(data []byte) (model.Template, error) {
	var f file
	if err := yaml.UnmarshalStrict(data, &f); err != nil {
		return model.Template{}, err
	}
	f.Spec.ApplyDefaults()
	t := model.Template{Name: f.Metadata.Name, Source: store.SourceBuiltin, Spec: f.Spec}
	return t, t.Validate()
}

// load reads and validates every *.yaml file in the root of files.
func load(files fs.FS) ([]model.Template, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read templates: %w", err)
	}
	var out []model.Template
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".yaml" {
			continue
		}
		data, err := fs.ReadFile(files, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		t, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if want := strings.TrimSuffix(e.Name(), ".yaml"); t.Name != want {
			return nil, fmt.Errorf("%s: metadata.name is %q, want %q", e.Name(), t.Name, want)
		}
		out = append(out, t)
	}
	return out, nil
}

// sameSpec compares what would be stored: the JSON of both. Comparing the Go
// values would differ for resource quantities, which cache their text.
func sameSpec(a, b model.TemplateSpec) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}
