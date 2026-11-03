// Package problem builds the API's RFC 9457 problem responses: every
// title comes from the reasons catalog, and validation findings become
// catalog field messages.
package problem

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Details is a problem whose text comes from the catalog. Errors huma
// builds itself are *huma.ErrorModel and are rewritten by Sanitize.
type Details struct {
	huma.ErrorModel
	// Reason fields are set when the problem is a catalog Reason.
	Reason  string `json:"reason,omitempty"`
	What    string `json:"what,omitempty"`
	Todo    string `json:"todo,omitempty"`
	Blame   string `json:"blame,omitempty"`
	Who     string `json:"who,omitempty"`
	headers http.Header
}

// Error is the catalog title, the text a person sees (stored on a plan that
// failed).
func (e *Details) Error() string { return e.Title }

// GetHeaders lets huma send the headers set with WithHeader.
func (e *Details) GetHeaders() http.Header { return e.headers }

// WithHeader sets a response header to send with the problem.
func (e *Details) WithHeader(k, v string) *Details {
	if e.headers == nil {
		e.headers = http.Header{}
	}
	e.headers.Set(k, v)
	return e
}

// New is the problem with this status and catalog title.
func New(status int, title string) *Details {
	return &Details{ErrorModel: huma.ErrorModel{Status: status, Title: title}}
}

// Reason renders the catalog Reason code as a problem.
func Reason(status int, code reasons.Code) *Details {
	r, _ := reasons.Lookup(code)
	e := New(status, r.Title)
	e.Reason, e.What, e.Todo, e.Blame, e.Who = string(r.Code), r.What, r.Todo, r.Blame, r.Who
	return e
}

// Internal logs err and returns the generic catalog 500.
func Internal(what string, err error) error {
	slog.Error(what, "err", err)
	return New(http.StatusInternalServerError, reasons.Msg(reasons.MsgServerError))
}

// Field is one form field error; Name is the request body field.
type Field struct{ Name, Msg string }

// Fields is the 422 problem listing these field errors.
func Fields(errs ...Field) *Details {
	e := New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgInvalidRequest))
	for _, f := range errs {
		loc := ""
		if f.Name != "" {
			loc = "body." + f.Name
		}
		e.Errors = append(e.Errors, &huma.ErrorDetail{Location: loc, Message: f.Msg})
	}
	return e
}

// FieldError is the 422 problem for one field with a catalog message.
func FieldError(field string, code reasons.MessageCode, args ...any) error {
	return Fields(Field{field, reasons.Msg(code, args...)})
}

// fieldMessages maps request body fields to the catalog message shown when
// a validator (huma's schema check or the API server) rejects them.
var fieldMessages = map[string]reasons.MessageCode{
	"displayName":          reasons.MsgNameInvalid,
	"template":             reasons.MsgTemplateUnavailable,
	"size":                 reasons.MsgSizeUnavailable,
	"apiAccess":            reasons.MsgAPIAccessInvalid,
	"idleStopMinutes":      reasons.MsgInvalidRequest,
	"command":              reasons.MsgCommandTooLong,
	"image":                reasons.MsgImageInvalid,
	"port":                 reasons.MsgPortInvalid,
	"file":                 reasons.MsgFileInvalid,
	"source":               reasons.MsgFileInvalid,
	"source.url":           reasons.MsgGitURLInvalid,
	"source.path":          reasons.MsgSourcePathInvalid,
	"source.ref":           reasons.MsgInvalidRequest,
	"source.build.command": reasons.MsgInvalidRequest,
	"source.build.output":  reasons.MsgSourcePathInvalid,
	"username":             reasons.MsgCredentialsMissing,
	"name":                 reasons.MsgTokenNameInvalid,
	"scope":                reasons.MsgTokenScopeInvalid,
	"expiresInDays":        reasons.MsgTokenExpiryInvalid,
	"password":             reasons.MsgCredentialsMissing,
}

// knownField maps a validator location to the one request field it names:
// "body.file" or "spec.file" give "file". Unknown locations give "".
func knownField(loc string) string {
	switch {
	case loc == "metadata.name":
		return "displayName"
	case strings.HasPrefix(loc, "body."):
		loc = strings.TrimPrefix(loc, "body.")
	case strings.HasPrefix(loc, "spec."):
		loc = strings.TrimPrefix(loc, "spec.")
	default:
		return ""
	}
	if _, ok := fieldMessages[loc]; ok {
		return loc
	}
	return ""
}

// Validation turns raw validator findings into catalog field errors:
// known fields get their message, the rest one generic message with no
// location. The raw text is logged, never returned.
func Validation(raw []*huma.ErrorDetail) []Field {
	var out []Field
	for _, d := range raw {
		slog.Info("request rejected by validation", "location", d.Location, "message", d.Message)
		f := Field{Msg: reasons.Msg(reasons.MsgInvalidRequest)}
		if name := knownField(d.Location); name != "" {
			f = Field{name, reasons.Msg(fieldMessages[name])}
			if name == "source.url" {
				f.Msg = reasons.Msg(GitURLCode(d.Message))
			}
		}
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		out = append(out, Field{Msg: reasons.Msg(reasons.MsgInvalidRequest)})
	}
	return out
}

// Sanitize replaces the text of every problem huma builds itself
// (schema validation, malformed bodies) with catalog copy.
func Sanitize(_ huma.Context, _ string, v any) (any, error) {
	em, ok := v.(*huma.ErrorModel)
	if !ok {
		return v, nil
	}
	if em.Status >= 500 {
		slog.Error("api error", "status", em.Status, "detail", em.Detail)
		return &huma.ErrorModel{Status: em.Status, Title: reasons.Msg(reasons.MsgServerError)}, nil
	}
	if em.Detail != "" {
		slog.Info("request rejected", "status", em.Status, "detail", em.Detail)
	}
	out := Fields(Validation(em.Errors)...)
	out.Status = em.Status
	return out, nil
}

// Team turns a placement refusal into the team field's message.
func Team(err error) error {
	var r *placement.Refusal
	if errors.As(err, &r) {
		return FieldError("team", r.Code)
	}
	return Internal("choose namespace", err)
}

// Cause is one rejected field, as model validation names it.
type Cause struct{ Field, Message string }

// Causes lists the fields err rejects; false when err is not a
// model.ValidationError.
func Causes(err error) ([]Cause, bool) {
	var ve *model.ValidationError
	if !errors.As(err, &ve) {
		return nil, false
	}
	out := make([]Cause, len(ve.Errors))
	for i, f := range ve.Errors {
		out[i] = Cause{f.Field, f.Message}
	}
	return out, true
}

// Invalid turns a model validation error into catalog field errors; nil
// when err is not one.
func Invalid(err error) error {
	causes, ok := Causes(err)
	if !ok {
		return nil
	}
	var raw []*huma.ErrorDetail
	for _, c := range causes {
		raw = append(raw, &huma.ErrorDetail{Location: c.Field, Message: c.Message})
	}
	return Fields(Validation(raw)...)
}

// Write writes an RFC 9457 problem with a catalog title, for
// handlers that run outside huma.
func Write(w http.ResponseWriter, status int, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(&huma.ErrorModel{Status: status, Title: title})
}

// GitURLCode picks the catalog message for a rejected repository address
// from the model's finding; a message never repeats the address, which may
// hold a password a person pasted by mistake.
func GitURLCode(finding string) reasons.MessageCode {
	switch {
	case strings.Contains(finding, "https address"):
		return reasons.MsgGitURLNotHTTPS
	case strings.Contains(finding, "user name or password"):
		return reasons.MsgGitURLHasCredentials
	}
	return reasons.MsgGitURLInvalid
}
