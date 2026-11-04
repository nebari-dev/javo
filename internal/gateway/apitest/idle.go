package apitest

import (
	"github.com/nebari-dev/javo/internal/config"
)

type IdleJSON struct {
	IdleStopMinutes  int32 `json:"idleStopMinutes"`
	StoppedIdleAfter int32 `json:"stoppedIdleAfter"`
}

// StrictIdle stops apps after an hour by default, between 10 minutes and
// 2 hours, and people may not turn it off.
func StrictIdle() config.IdleStopCfg {
	no := false
	return config.IdleStopCfg{DefaultMinutes: 60, MinMinutes: 10, MaxMinutes: 120, AllowOff: &no}
}

// OptionalIdle is the chart's policy: Off by default, between 10 minutes and
// 7 days, and people may turn it off.
func OptionalIdle() config.IdleStopCfg {
	return config.IdleStopCfg{MinMinutes: 10, MaxMinutes: 10080}
}
