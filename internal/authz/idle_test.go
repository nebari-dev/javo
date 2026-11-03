package authz_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/reasons"
)

// The idle stop a person picks must be in the admin's range, and Off only
// when the admin allows it; platform admins may always turn it off.
func TestIdleStop(t *testing.T) {
	no := false
	strict := config.IdleStopCfg{DefaultMinutes: 60, MinMinutes: 10, MaxMinutes: 120, AllowOff: &no}
	open := config.IdleStopCfg{MinMinutes: 10, MaxMinutes: 120}
	for _, tc := range []struct {
		name    string
		p       authz.Principal
		policy  config.IdleStopCfg
		minutes int32
		want    bool
		code    reasons.MessageCode
	}{
		{"in range", alice, strict, 30, true, ""},
		{"at the bounds", alice, strict, 120, true, ""},
		{"too short", alice, strict, 5, false, reasons.MsgIdleStopOutOfRange},
		{"too long", alice, strict, 121, false, reasons.MsgIdleStopOutOfRange},
		{"off when allowed", alice, open, 0, true, ""},
		{"off when not allowed", alice, strict, 0, false, reasons.MsgIdleStopOffNotAllowed},
		{"admin turns it off anyway", admin, strict, 0, true, ""},
		{"admin still in range", admin, strict, 500, false, reasons.MsgIdleStopOutOfRange},
		{"negative", alice, open, -1, false, reasons.MsgIdleStopOutOfRange},
		{"anonymous", anon, open, 30, false, reasons.MsgSignInRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := decide(tc.p, authz.ActIdleStop, authz.Object{Idle: &authz.IdleAsk{Minutes: tc.minutes, Policy: tc.policy}})
			if d.Allowed != tc.want || d.Code != tc.code {
				t.Fatalf("got %+v", d)
			}
		})
	}
}
