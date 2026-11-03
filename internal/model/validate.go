package model

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// FieldError is one rejected field. Field is addressed like the old custom
// resource fields (spec.schedule.timeZone, spec.parameters[0].name) and is
// what the gateway turns into a catalog message for the form field.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError lists every rule a record broke.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, f := range e.Errors {
		parts[i] = f.Field + ": " + f.Message
	}
	return strings.Join(parts, "; ")
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validator collects field errors.
type validator struct{ errs []FieldError }

func (v *validator) add(field, msg string) { v.errs = append(v.errs, FieldError{field, msg}) }

func (v *validator) err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

// merge adds the errors of other (nil or a *ValidationError) and returns the result.
func (v *validator) merge(other error) error {
	if ve, ok := other.(*ValidationError); ok {
		v.errs = append(v.errs, ve.Errors...)
	}
	return v.err()
}

// owner checks a person name that goes into label values and claim names.
// name requires the short name an app or job is addressed by.
func (v *validator) name(name string) {
	if name == "" {
		v.add("metadata.name", "name is required")
	}
}

func (v *validator) owner(field, name string) {
	if name == "" || len(name) > 63 || !dnsLabel.MatchString(name) {
		v.add(field, fmt.Sprintf("invalid value %q: an owner is a person name, a lowercase DNS label of 1 to 63 characters", name))
	}
}

func (v *validator) immutable(field, msg string, now, before string) {
	if now != before {
		v.add(field, msg)
	}
}

func (v *validator) maxLen(field, value string, n int) {
	if charLen(value) > n {
		v.add(field, fmt.Sprintf("must be at most %d characters", n))
	}
}

func (v *validator) maxItems(field string, have, n int) {
	if have > n {
		v.add(field, fmt.Sprintf("must have at most %d items", n))
	}
}

// oneOf requires value to be one of allowed; an empty value passes when
// emptyOK (a field with a default).
func (v *validator) oneOf(field, value string, emptyOK bool, allowed ...string) {
	if value == "" && emptyOK || slices.Contains(allowed, value) {
		return
	}
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	v.add(field, fmt.Sprintf("unsupported value %q, supported values: %s", value, strings.Join(quoted, ", ")))
}

func indexed(field string, i int) string { return fmt.Sprintf("%s[%d]", field, i) }

// charLen counts characters, the way the old CRD MaxLength did, not bytes.
func charLen(s string) int { return utf8.RuneCountInString(s) }
