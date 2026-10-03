// Command releaselicenses collects upstream notices for binary distributions.
// Output lives in the build cache; no generated notice file is tracked in Git.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type module struct {
	Path, Version, Dir string
	Main               bool
}

func main() {
	output := flag.String("out", ".cache/release/NOTICE.txt", "generated notice file")
	flag.Parse()
	if err := run(*output); err != nil {
		fmt.Fprintln(os.Stderr, "release licenses:", err)
		os.Exit(1)
	}
}

func run(output string) error {
	cmd := exec.Command("go", "list", "-mod=readonly", "-deps", "-json", "./cmd/geocode")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Stderr = os.Stderr
	data, err := cmd.Output()
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	modules := make(map[string]module)
	for {
		var pkg struct{ Module *module }
		err := decoder.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if pkg.Module != nil && !pkg.Module.Main {
			modules[pkg.Module.Path] = *pkg.Module
		}
	}
	var notice strings.Builder
	notice.WriteString("Third-party notices for geocoding-go\n\nGenerated from the Go runtime, linked modules, and local PBF decoder sources.\nUpstream notices are retained in full, including notices for components that\nmay not be linked into this binary. OSM datasets are not bundled.\n")
	appendNotice := func(title, path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&notice, "\n========================================================================\n%s\n========================================================================\n\n%s\n", title, data)
		return nil
	}
	goRoot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return err
	}
	if err := appendNotice("Go runtime and standard library - LICENSE", filepath.Join(strings.TrimSpace(string(goRoot)), "LICENSE")); err != nil {
		return err
	}
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		m := modules[path]
		files, err := os.ReadDir(m.Dir)
		if err != nil {
			return err
		}
		found := false
		for _, file := range files {
			upper := strings.ToUpper(file.Name())
			isNotice := strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "NOTICE") || strings.HasPrefix(upper, "THIRD-PARTY-NOTICES")
			if file.IsDir() || !isNotice {
				continue
			}
			if err := appendNotice(m.Path+" "+m.Version+" - "+file.Name(), filepath.Join(m.Dir, file.Name())); err != nil {
				return err
			}
			found = true
		}
		if !found {
			return fmt.Errorf("no license found for %s; update notice collection before releasing", m.Path)
		}
	}
	if err := appendNotice("Local PBF decoder fork - LICENSE.md", "internal/pbf/LICENSE.md"); err != nil {
		return err
	}
	proto, err := os.ReadFile("internal/pbf/internal/osmpbf/fileformat.proto")
	if err != nil {
		return err
	}
	header, _, ok := strings.Cut(string(proto), "*/")
	if !ok || !strings.Contains(header, "Copyright") {
		return errors.New("missing OSM PBF protocol copyright notice")
	}
	fmt.Fprintf(&notice, "\nOSM PBF protocol definitions and generated code\n\n%s\n", strings.TrimPrefix(header, "/** "))
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(output, []byte(strings.ReplaceAll(notice.String(), "\r\n", "\n")), 0o644)
}
