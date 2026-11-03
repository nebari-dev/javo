package apitest

import (
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

type RestoresJSON struct {
	Items []struct {
		Name       string `json:"name"`
		Held       int    `json:"held"`
		WasRunning int    `json:"wasRunning"`
		Scheduled  int    `json:"scheduled"`
	} `json:"items"`
}

func HeldApp(owner, name, hold string, paused bool) *model.App {
	a := gatewaytest.App(owner, name)
	a.RestoreHold, a.Paused = hold, paused
	return a
}
