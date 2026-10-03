// Command releasesmoke checks release checksums and exercises the packaged host
// binary against a generated PBF. It needs no downloaded dataset or credentials.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func main() {
	dist := flag.String("dist", "dist", "GoReleaser output directory")
	flag.Parse()
	if err := run(*dist); err != nil {
		fmt.Fprintln(os.Stderr, "release smoke test:", err)
		os.Exit(1)
	}
}

func run(dist string) error {
	archive, err := hostArchive(dist)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "geocode-release-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	binaryName := "geocode"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	if err := extract(archive, dir, binaryName); err != nil {
		return err
	}
	binary := filepath.Join(dir, binaryName)
	version, err := command(binary, "--version")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(version), "geocode ") || bytes.Contains(version, []byte("unknown")) || bytes.Contains(version, []byte("geocode dev ")) {
		return fmt.Errorf("missing release metadata: %s", version)
	}
	for _, args := range [][]string{{"--help"}, {"import", "-h"}, {"serve", "-h"}} {
		if _, err := command(binary, args...); err != nil {
			return err
		}
	}
	pbf, db := filepath.Join(dir, "fixture.pbf"), filepath.Join(dir, "fixture.db")
	if err := os.WriteFile(pbf, fixturePBF(), 0o600); err != nil {
		return err
	}
	if _, err := command(binary, "import", "-pbf", pbf, "-db", db, "-workers", "2"); err != nil {
		return err
	}
	stats, err := command(binary, "stats", "-db", db, "-verify")
	if err != nil {
		return err
	}
	var count struct {
		Features int `json:"features"`
	}
	if err := json.Unmarshal(stats, &count); err != nil || count.Features != 1 {
		return fmt.Errorf("unexpected database stats: %s", stats)
	}
	for _, args := range [][]string{
		{"search", "-db", db, "-q", "Teststrasse 10 Teststadt"},
		{"reverse", "-db", db, "-lat", "47.3769", "-lon", "8.5417"},
	} {
		out, err := command(binary, args...)
		if err != nil {
			return err
		}
		if err := checkResult(out, true); err != nil {
			return err
		}
	}
	if _, err := command(binary, "import", "-pbf", pbf, "-db", db); err == nil {
		return errors.New("import unexpectedly overwrote an existing database")
	}
	if err := checkServer(binary, db); err != nil {
		return err
	}
	fmt.Printf("release smoke test passed: %s (%s/%s)\n%s", filepath.Base(archive), runtime.GOOS, runtime.GOARCH, version)
	return nil
}

// Validate all six platform archives, then choose the executable host archive.
func hostArchive(dist string) (string, error) {
	manifest, err := os.ReadFile(filepath.Join(dist, "artifacts.json"))
	if err != nil {
		return "", err
	}
	var artifacts []struct{ Path, Goos, Goarch, Type string }
	if err := json.Unmarshal(manifest, &artifacts); err != nil {
		return "", err
	}
	checksums, err := os.ReadFile(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		return "", err
	}
	expected := make(map[string]string)
	for line := range strings.Lines(string(checksums)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 || filepath.Base(fields[1]) != fields[1] {
			return "", fmt.Errorf("invalid checksum entry %q", line)
		}
		expected[fields[1]] = fields[0]
	}
	platforms := make(map[string]bool)
	var host string
	for _, a := range artifacts {
		if a.Type != "Archive" {
			continue
		}
		name := filepath.Base(filepath.FromSlash(a.Path))
		path := filepath.Join(dist, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != expected[name] {
			return "", fmt.Errorf("checksum mismatch for %s", name)
		}
		platforms[a.Goos+"/"+a.Goarch] = true
		if a.Goos == runtime.GOOS && a.Goarch == runtime.GOARCH {
			host = path
		}
	}
	for _, osName := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			if !platforms[osName+"/"+arch] {
				return "", fmt.Errorf("missing archive for %s/%s", osName, arch)
			}
		}
	}
	if host == "" {
		return "", fmt.Errorf("no archive for host %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return host, nil
}

func extract(archive, dir, binary string) error {
	wanted := map[string]bool{binary: false, "LICENSE": false, "NOTICE.txt": false,
		"README.md": false, "ARCHITECTURE.md": false}
	write := func(name string, source io.Reader) error {
		seen, allowed := wanted[name]
		if !allowed || seen {
			return fmt.Errorf("unexpected or duplicate archive entry %q", name)
		}
		// All allowed names are fixed basenames; archives cannot escape dir.
		data, err := io.ReadAll(io.LimitReader(source, (64<<20)+1))
		if err != nil {
			return err
		}
		if len(data) == 0 || len(data) > 64<<20 {
			return fmt.Errorf("invalid size for archive entry %q", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o700); err != nil {
			return err
		}
		wanted[name] = true
		return nil
	}
	if strings.HasSuffix(archive, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, entry := range z.File {
			if !entry.Mode().IsRegular() {
				return fmt.Errorf("non-file archive entry %q", entry.Name)
			}
			r, err := entry.Open()
			if err != nil {
				return err
			}
			err = write(entry.Name, r)
			if err := errors.Join(err, r.Close()); err != nil {
				return err
			}
		}
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if header.Typeflag != tar.TypeReg {
				return fmt.Errorf("non-file archive entry %q", header.Name)
			}
			if err := write(header.Name, tr); err != nil {
				return err
			}
		}
	}
	for name, seen := range wanted {
		if !seen {
			return fmt.Errorf("archive is missing %s", name)
		}
	}
	return nil
}

func command(binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("geocode %v: %w\n%s%s", args, err, out, stderr.String())
	}
	return out, nil
}

func checkResult(data []byte, array bool) error {
	type result struct {
		OSMID int64   `json:"osm_id"`
		Lat   float64 `json:"lat"`
		Lon   float64 `json:"lon"`
	}
	var r result
	if array {
		var results []result
		if err := json.Unmarshal(data, &results); err != nil || len(results) != 1 {
			return fmt.Errorf("expected one result: %s", data)
		}
		r = results[0]
	} else if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if r.OSMID != 42 || r.Lat != 47.3769 || r.Lon != 8.5417 {
		return fmt.Errorf("unexpected geocoding result: %s", data)
	}
	return nil
}

func checkServer(binary, db string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "serve", "-db", db, "-addr", address)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	defer func() {
		cancel()
		<-wait
	}()
	// Keep the HTTP client independent of developer proxy settings.
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: time.Second, Transport: transport}
	base := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := client.Get(base + "/health")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("packaged server did not become healthy")
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, check := range []struct {
		path  string
		array bool
	}{
		{"/search?q=Teststrasse%2010%20Teststadt", true},
		{"/reverse?lat=47.3769&lon=8.5417", false},
	} {
		res, err := client.Get(base + check.path)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		err = errors.Join(readErr, res.Body.Close())
		if err != nil {
			return err
		}
		if res.StatusCode != http.StatusOK || res.Header.Get("X-Data-Attribution") == "" {
			return fmt.Errorf("HTTP %s: status=%d, attribution=%q", check.path, res.StatusCode, res.Header.Get("X-Data-Attribution"))
		}
		if err := checkResult(data, check.array); err != nil {
			return err
		}
	}
	if runtime.GOOS != "windows" {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return err
		}
		select {
		case err := <-wait:
			wait <- err // Let deferred cleanup consume the result.
			if err != nil {
				return fmt.Errorf("server shutdown: %w", err)
			}
		case <-time.After(12 * time.Second):
			return errors.New("server did not shut down after SIGTERM")
		}
	}
	return nil
}
