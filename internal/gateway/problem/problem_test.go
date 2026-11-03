package problem

import (
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"net/http"
	"testing"
)

// A rejection from model validation reaches the form on the request field it
// is about, with the catalog's words and never the validator's.
func TestModelValidationErrorsReachTheFormByField(t *testing.T) {
	for _, tc := range []struct {
		field string
		form  string
		msg   reasons.MessageCode
	}{
		{"spec.command", "body.command", reasons.MsgCommandTooLong},
		{"spec.image", "body.image", reasons.MsgImageInvalid},
		{"spec.port", "body.port", reasons.MsgPortInvalid},
		{"spec.size", "body.size", reasons.MsgSizeUnavailable},
		{"spec.owner", "", reasons.MsgInvalidRequest},
	} {
		ours := &model.ValidationError{Errors: []model.FieldError{{Field: tc.field, Message: "internal detail"}}}
		got, ok := Invalid(ours).(*Details)
		if !ok || len(got.Errors) != 1 || got.Errors[0].Location != tc.form || got.Errors[0].Message != reasons.Msg(tc.msg) {
			t.Errorf("%s: got %+v", tc.field, Invalid(ours))
		}
	}
	if Invalid(New(http.StatusNotFound, reasons.Msg(reasons.MsgAppNotFound))) != nil {
		t.Error("an error that is not a validation error was mapped")
	}
}
