// Package transfer writes apps as YAML documents a person can keep, and
// reads them back. A document holds only what a person chose for the app:
// never its id, owner, namespace, version, times or status, so an import
// always belongs to whoever imports it. Sharing is left out too: an
// imported app starts private, and its owner shares it again.
package transfer

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/resource"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/nebari-dev/javo/internal/model"
)

// APIVersion and Kind name an app document.
const (
	APIVersion = model.Group + "/v1alpha1"
	Kind       = "App"
)

// MaxDocuments is the most documents one file may hold; MaxBytes the
// largest file.
const (
	MaxDocuments = 500
	MaxBytes     = 1 << 20
)

// ErrTooMany is returned by Parse for a file of more than MaxDocuments
// documents; ErrUnreadable for one that is not YAML documents at all.
var (
	ErrTooMany    = errors.New("transfer: too many documents")
	ErrUnreadable = errors.New("transfer: not a YAML file")
)

// Doc is one app.
type Doc struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec Spec `json:"spec"`
}

// Spec is what a person chose for the app.
type Spec struct {
	DisplayName     string     `json:"displayName"`
	Template        string     `json:"template"`
	File            string     `json:"file,omitempty"`
	Sample          bool       `json:"sample,omitempty"`
	Size            string     `json:"size,omitempty"`
	Resources       *Resources `json:"resources,omitempty"`
	Command         string     `json:"command,omitempty"`
	Image           string     `json:"image,omitempty"`
	Port            int32      `json:"port,omitempty"`
	APIAccess       string     `json:"apiAccess,omitempty"`
	IdleStopMinutes int32      `json:"idleStopMinutes,omitempty"`
	Paused          bool       `json:"paused,omitempty"`
	// Source is the Git repository the app runs from; absent for a home
	// folder app. A token for a private repository is never written.
	Source *model.Source `json:"source,omitempty"`
	// Env are the app's environment variables: plain values as they are, and
	// values from secrets by the secret's name. A secret's value is never
	// written: an import binds the importer's own secret of that name.
	Env []Env `json:"env,omitempty"`
	// Secrets is how files written before Env named the secrets an app
	// gets; it is read, never written.
	Secrets []Secret `json:"secrets,omitempty"`
}

// Env is one environment variable: a plain value, or a secret by name.
type Env struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret string `json:"secret,omitempty"`
}

// Secret is one secret an app gets, by name (files written before Env).
type Secret struct {
	Env    string `json:"env"`
	Secret string `json:"secret"`
}

// Resources is a custom CPU and memory, as Kubernetes quantities.
type Resources struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// Export writes the apps as YAML documents, one per app, in the order given,
// with their plain variables and the names of the secrets each gets
// (secrets, by app id).
func Export(apps []model.App, secrets map[string][]model.SecretBinding) ([]byte, error) {
	var b bytes.Buffer
	for i := range apps {
		d := docOf(&apps[i])
		for _, e := range apps[i].Env {
			d.Spec.Env = append(d.Spec.Env, Env{Name: e.Name, Value: e.Value})
		}
		for _, s := range secrets[apps[i].ID] {
			d.Spec.Env = append(d.Spec.Env, Env{Name: s.Env, Secret: s.Secret})
		}
		out, err := yaml.Marshal(d)
		if err != nil {
			return nil, err
		}
		b.WriteString("---\n")
		b.Write(out)
	}
	return b.Bytes(), nil
}

func docOf(a *model.App) Doc {
	d := Doc{APIVersion: APIVersion, Kind: Kind}
	d.Metadata.Name = a.Name
	d.Spec = Spec{
		DisplayName: a.DisplayName, Template: a.Template, File: a.File, Sample: a.Sample, Size: a.Size,
		Command: a.Command, Image: a.Image, Port: a.Port, APIAccess: a.APIAccess,
		IdleStopMinutes: a.IdleStopMinutes, Paused: a.Paused,
	}
	if src := a.Source; src != nil {
		c := *src
		c.Fork = false
		d.Spec.Source = &c
	}
	if r := a.Resources; r != nil {
		d.Spec.Resources = &Resources{CPU: r.CPU.String(), Memory: r.Memory.String()}
	}
	return d
}

// Item is one document of a file: Doc, or Err when that document is not a
// valid app document.
type Item struct {
	Doc *Doc
	Err error
}

// Parse reads the documents of an exported file. Each document is decoded
// strictly (an unknown field is an error) and on its own, so one bad document
// does not hide the others. More than MaxDocuments is ErrTooMany. Empty
// documents are skipped.
func Parse(data []byte) ([]Item, error) {
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var items []Item
	for {
		raw, err := r.Read()
		if errors.Is(err, io.EOF) {
			return items, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
		}
		if len(bytes.TrimSpace(raw)) == 0 || isOnlyComments(raw) {
			continue
		}
		if len(items) == MaxDocuments {
			return nil, ErrTooMany
		}
		// Broken YAML means a broken file, not one bad entry.
		var any interface{}
		if err := yaml.Unmarshal(raw, &any); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
		}
		items = append(items, parseOne(raw))
	}
}

func parseOne(raw []byte) Item {
	var d Doc
	if err := yaml.UnmarshalStrict(raw, &d); err != nil {
		return Item{Err: err}
	}
	if d.APIVersion != APIVersion || d.Kind != Kind {
		return Item{Err: fmt.Errorf("apiVersion %q, kind %q is not %s, %s", d.APIVersion, d.Kind, APIVersion, Kind)}
	}
	return Item{Doc: &d}
}

func isOnlyComments(raw []byte) bool {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if l := bytes.TrimSpace(line); len(l) > 0 && l[0] != '#' {
			return false
		}
	}
	return true
}

// EnvVars are the variables the document asks for, the secrets of a file
// written before Env among them.
func (d *Doc) EnvVars() []Env {
	out := append([]Env(nil), d.Spec.Env...)
	for _, s := range d.Spec.Secrets {
		out = append(out, Env{Name: s.Env, Secret: s.Secret})
	}
	return out
}

// ToApp is the app the document describes, with no owner, namespace or name:
// the importer sets those. A custom amount that does not parse is an error.
func (d *Doc) ToApp() (model.App, error) {
	s := d.Spec
	a := model.App{
		DisplayName: s.DisplayName, Template: s.Template, File: s.File, Sample: s.Sample, Size: s.Size,
		Command: s.Command, Image: s.Image, Port: s.Port, APIAccess: s.APIAccess,
		IdleStopMinutes: s.IdleStopMinutes, Paused: s.Paused, Source: s.Source,
	}
	if r := s.Resources; r != nil {
		cpu, err := resource.ParseQuantity(r.CPU)
		if err != nil {
			return model.App{}, fmt.Errorf("spec.resources.cpu: %w", err)
		}
		mem, err := resource.ParseQuantity(r.Memory)
		if err != nil {
			return model.App{}, fmt.Errorf("spec.resources.memory: %w", err)
		}
		a.Resources = &model.AppResources{CPU: cpu, Memory: mem}
	}
	return a, nil
}
