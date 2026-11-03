package pixienv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RegisterKernels offers the Jupyter kernels installed in the environment at
// prefix (prefix/share/jupyter/kernels/*/kernel.json) to a server that reads
// the Jupyter data folder to: each becomes to/kernels/javo-<Slug(name)>-<kernel>,
// named after the environment and started in its activation (the script
// activate) so its PATH and activation scripts apply. When the environment
// has no kernel and notice is set, notice gets the sentence that says so;
// that is not an error, the server still starts.
func RegisterKernels(prefix, activate, name, to, notice string) error {
	specs, err := filepath.Glob(filepath.Join(prefix, "share", "jupyter", "kernels", "*", "kernel.json"))
	if err != nil {
		return err
	}
	sort.Strings(specs)
	if len(specs) == 0 {
		if notice == "" {
			return nil
		}
		msg := fmt.Sprintf("The environment %s has no Jupyter kernel, so JupyterLab does not offer it. Add ipykernel (or another kernel) to it, then restart the app.", name)
		return os.WriteFile(notice, []byte(msg), 0o644)
	}
	for _, p := range specs {
		src := filepath.Dir(p)
		kernel := filepath.Base(src)
		var spec map[string]any
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &spec); err != nil {
			return fmt.Errorf("kernel %s: %w", kernel, err)
		}
		argv, _ := spec["argv"].([]any)
		spec["argv"] = append([]any{"/bin/sh", "-c", `. "$0" && exec "$@"`, activate}, argv...)
		spec["display_name"] = name
		if len(specs) > 1 {
			orig, _ := spec["display_name"].(string)
			if d, ok := origDisplay(b); ok {
				orig = d
			}
			spec["display_name"] = fmt.Sprintf("%s (%s)", name, orig)
		}
		dst := filepath.Join(to, "kernels", "javo-"+Slug(name)+"-"+kernel)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || e.Name() == "kernel.json" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
				return err
			}
		}
		out, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, "kernel.json"), out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// origDisplay is the display_name a kernel.json gave itself.
func origDisplay(raw []byte) (string, bool) {
	var s struct {
		DisplayName string `json:"display_name"`
	}
	if json.Unmarshal(raw, &s) != nil || s.DisplayName == "" {
		return "", false
	}
	return s.DisplayName, true
}

// ProgramOf is the program a container with command and args starts, for
// Source.Program: the first word after pod-user.sh (pod/files), empty when
// that is a shell running a script or there is no command.
func ProgramOf(command, args []string) string {
	argv := append(append([]string{}, command...), args...)
	if len(argv) > 0 && filepath.Base(argv[0]) == "pod-user.sh" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return ""
	}
	switch filepath.Base(argv[0]) {
	case "sh", "bash", "dash", "zsh":
		return ""
	}
	return argv[0]
}
