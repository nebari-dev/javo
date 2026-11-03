package apitest

import (
	"strings"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

// CustomTemplate takes a typed command; its sample runs with sampleCommand.
func CustomTemplate() *model.Template {
	t := gatewaytest.Template("custom", "custom")
	t.Spec.UserCommand = model.UserCommandRequired
	t.Spec.Sample = &model.TemplateSample{Path: "/opt/javo/samples/custom", Title: "Server time", Command: SampleCommand}
	return t
}

const SampleCommand = "python sample.py --port={{.Port}} --base-url={{.BaseURL}}"

// ContainerTemplate runs the image each app names.
func ContainerTemplate() *model.Template {
	t := gatewaytest.Template("container", "container")
	t.Spec.Image, t.Spec.Port, t.Spec.UserImage, t.Spec.BasePath = "", 8080, true, "Strip"
	t.Spec.UserCommand = model.UserCommandOptional
	return t
}

type LaunchJSON struct {
	WorkspaceJSON
	Command string `json:"command"`
	Image   string `json:"image"`
	Port    int32  `json:"port"`
}

// ContainerSample is the container template's sample: an image, its port
// and a command.
func ContainerSample() *model.Template {
	t := ContainerTemplate()
	t.Spec.Sample = &model.TemplateSample{Title: "Toolbox", Image: "example.com/toolbox:1", Port: 9000, Command: "serve --port={{.Port}}"}
	return t
}

func Quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
