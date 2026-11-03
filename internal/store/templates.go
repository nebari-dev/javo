package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Templates is the templates table.
type Templates struct{ s *Store }

// Templates returns the templates table.
func (s *Store) Templates() *Templates { return &Templates{s} }

// Template sources.
const (
	SourceBuiltin = "builtin"
	SourceAdmin   = "admin"
)

// Put saves a template and returns it. expectedVersion 0 creates it and a
// taken name is ErrExists; any other value updates the template only if its
// stored version is still expectedVersion (else ErrConflict, or ErrNotFound
// if it is gone) and raises the version by one. The source must be builtin or
// admin and cannot change: a builtin save never replaces an admin template,
// nor the other way round (ErrExists on create, ErrConflict on update).
func (t *Templates) Put(ctx context.Context, actor model.Actor, tpl model.Template, expectedVersion int64) (model.Template, error) {
	if tpl.Source != SourceBuiltin && tpl.Source != SourceAdmin {
		return model.Template{}, &model.ValidationError{Errors: []model.FieldError{{Field: "source", Message: "source must be builtin or admin"}}}
	}
	if err := tpl.Validate(); err != nil {
		return model.Template{}, err
	}
	spec, err := marshal(tpl.Spec, "{}")
	if err != nil {
		return model.Template{}, err
	}
	var row sqlcgen.Template
	err = t.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		var err error
		at := dbtime.From(now())
		if expectedVersion == 0 {
			row, err = q.InsertTemplate(ctx, sqlcgen.InsertTemplateParams{Name: tpl.Name, Source: tpl.Source, Spec: spec, UpdatedAt: at})
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrExists
			}
		} else {
			row, err = q.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{
				Spec: spec, UpdatedAt: at, Name: tpl.Name, Version: expectedVersion, Source: tpl.Source,
			})
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrConflict
				if _, getErr := q.GetTemplate(ctx, tpl.Name); errors.Is(getErr, sql.ErrNoRows) {
					err = ErrNotFound
				}
			}
		}
		return effect{audit: &auditRow{action: "template.put", kind: "template", name: tpl.Name}, changes: []Change{{"templates", tpl.Name}}}, err
	})
	if err != nil {
		return model.Template{}, err
	}
	return templateOf(row)
}

// Get returns the template or ErrNotFound.
func (t *Templates) Get(ctx context.Context, name string) (model.Template, error) {
	row, err := t.s.q.GetTemplate(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Template{}, ErrNotFound
	}
	if err != nil {
		return model.Template{}, err
	}
	return templateOf(row)
}

// List returns every template ordered by name.
func (t *Templates) List(ctx context.Context) ([]model.Template, error) {
	rows, err := t.s.q.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Template, len(rows))
	for i, r := range rows {
		if out[i], err = templateOf(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Delete removes a template, or returns ErrNotFound.
func (t *Templates) Delete(ctx context.Context, actor model.Actor, name string) error {
	return t.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.DeleteTemplate(ctx, name)
		if err == nil && n == 0 {
			err = ErrNotFound
		}
		return effect{audit: &auditRow{action: "template.delete", kind: "template", name: name}, changes: []Change{{"templates", name}}}, err
	})
}

func templateOf(r sqlcgen.Template) (model.Template, error) {
	tpl := model.Template{Name: r.Name, Source: r.Source, Version: r.Version}
	return tpl, unmarshal(r.Spec, &tpl.Spec)
}
