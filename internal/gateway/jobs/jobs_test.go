package jobs

import (
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
)

func TestSaveErrorMapsModelScheduleErrorsToScheduleFields(t *testing.T) {
	t.Parallel()
	s := &API{}
	b := &jobBody{Schedule: &JobScheduleBody{Cron: "0 25 * * *", TimeZone: "Mars/Base"}}
	for _, c := range []struct{ model, location string }{
		{"spec.schedule.cron", "body.schedule.cron"},
		{"spec.schedule.timeZone", "body.schedule.timeZone"},
	} {
		ours := &model.ValidationError{Errors: []model.FieldError{{Field: c.model, Message: "bad"}}}
		got, ok := s.saveError(ours, b).(*problem.Details)
		if !ok || len(got.Errors) != 1 || got.Errors[0].Location != c.location {
			t.Errorf("error on %s: got %+v, want one error at %s", c.model, s.saveError(ours, b), c.location)
		}
	}
}
