package apitest

import (
	"time"
)

type UsageJSON struct {
	Since  time.Time `json:"since"`
	Totals struct {
		Hours      float64 `json:"hours"`
		People     int     `json:"people"`
		RunningNow int     `json:"runningNow"`
	} `json:"totals"`
	Items []struct {
		Key        string  `json:"key"`
		Hours      float64 `json:"hours"`
		Apps       int     `json:"apps"`
		RunningNow int     `json:"runningNow"`
	} `json:"items"`
}
