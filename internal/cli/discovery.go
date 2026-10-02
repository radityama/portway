package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/radityama/portway/internal/config"
)

var ErrNoService = errors.New("no local development service found; specify a port")
var ErrAmbiguous = errors.New("multiple local development services found; specify a port")
var scriptPort = regexp.MustCompile(`(?:^|\s)(?:--port(?:=|\s+)|-p\s+|PORT=)([1-9][0-9]{0,4})(?:\s|$)`)
var localURL = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|\[::1\]):([1-9][0-9]{0,4})(?:/|\s|$)`)

type Detection struct {
	Port           int    `json:"port"`
	Source         string `json:"source"`
	PackageManager string `json:"package_manager,omitempty"`
}

func boundedLocalFile(path string, limit int64, recent bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("local discovery input must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("local discovery input changed")
	}
	if !recent && opened.Size() > limit {
		return nil, errors.New("local discovery input exceeds its size limit")
	}
	if recent && opened.Size() > limit {
		if _, err = f.Seek(-limit, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, limit))
}
func validPort(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != raw {
		return 0, errors.New("invalid local port")
	}
	return n, nil
}
func Detect(ctx context.Context, env config.Lookup, directory string) (Detection, error) {
	if raw, ok := env("PORTWAY_LOCAL_PORT"); ok && raw != "" {
		port, err := validPort(raw)
		if err != nil {
			return Detection{}, err
		}
		if !Reachable(ctx, port) {
			return Detection{}, ErrNoService
		}
		return Detection{Port: port, Source: "configuration"}, nil
	}
	var candidates []int
	source, manager := "loopback_probe", ""
	raw, err := boundedLocalFile(filepath.Join(directory, "package.json"), 64*1024, false)
	if err == nil {
		var pkg struct {
			PackageManager  string                     `json:"packageManager"`
			Scripts         map[string]string          `json:"scripts"`
			Dependencies    map[string]json.RawMessage `json:"dependencies"`
			DevDependencies map[string]json.RawMessage `json:"devDependencies"`
		}
		if json.Unmarshal(raw, &pkg) != nil {
			return Detection{}, errors.New("invalid package.json for discovery; specify a port")
		}
		for _, name := range []string{"npm", "pnpm", "yarn", "bun"} {
			if strings.HasPrefix(pkg.PackageManager, name+"@") {
				manager = name
			}
		}
		if manager == "" {
			for _, entry := range []struct{ file, name string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"}} {
				if info, err := os.Lstat(filepath.Join(directory, entry.file)); err == nil && info.Mode().IsRegular() {
					manager = entry.name
					break
				}
			}
		}
		if manager == "" {
			manager = "npm"
		}
		dev := pkg.Scripts["dev"]
		if dev == "" {
			dev = pkg.Scripts["start"]
		}
		if match := scriptPort.FindStringSubmatch(dev); match != nil {
			port, err := validPort(match[1])
			if err != nil {
				return Detection{}, err
			}
			candidates = []int{port}
			source = "dev_script"
		} else {
			for _, framework := range []struct {
				name string
				port int
			}{{"next", 3000}, {"vite", 5173}, {"nuxt", 3000}, {"astro", 4321}, {"@angular/cli", 4200}, {"react-scripts", 3000}, {"sveltekit", 5173}, {"@sveltejs/kit", 5173}} {
				_, prod := pkg.Dependencies[framework.name]
				_, devDep := pkg.DevDependencies[framework.name]
				if prod || devDep || strings.Contains(dev, framework.name+" ") {
					candidates = append(candidates, framework.port)
				}
			}
			if len(candidates) > 0 {
				source = "framework"
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Detection{}, errors.New("cannot read bounded package.json for discovery; specify a port")
	}
	if logFile, ok := env("PORTWAY_DEV_LOG_FILE"); ok && logFile != "" {
		raw, err := boundedLocalFile(logFile, 64*1024, true)
		if err != nil {
			return Detection{}, errors.New("cannot read bounded development output; specify a port")
		}
		matches := localURL.FindAllSubmatch(raw, 128)
		if len(matches) > 0 {
			// Prefer explicit script configuration; otherwise recent observed local
			// URLs precede framework defaults and the final generic probe set.
			if source != "dev_script" {
				candidates = nil
				source = "dev_output"
				for _, match := range matches {
					if port, err := validPort(string(match[1])); err == nil {
						candidates = append(candidates, port)
					}
				}
			}
		}
	}
	if len(candidates) == 0 {
		candidates = []int{3000, 3001, 4000, 4200, 4321, 5000, 5173, 8000, 8080, 8888}
	}
	unique := map[int]bool{}
	var ports []int
	for _, port := range candidates {
		if !unique[port] {
			unique[port] = true
			ports = append(ports, port)
		}
	}
	if len(ports) > 128 {
		return Detection{}, ErrAmbiguous
	}
	sort.Ints(ports)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	var reachable []int
	var workers sync.WaitGroup
	jobs := make(chan int, len(ports))
	for _, port := range ports {
		jobs <- port
	}
	close(jobs)
	for range min(4, len(ports)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for port := range jobs {
				if Reachable(ctx, port) {
					mu.Lock()
					reachable = append(reachable, port)
					mu.Unlock()
				}
			}
		}()
	}
	workers.Wait()
	if ctx.Err() != nil {
		return Detection{}, ctx.Err()
	}
	if len(reachable) == 0 {
		return Detection{}, ErrNoService
	}
	if len(reachable) != 1 {
		return Detection{}, ErrAmbiguous
	}
	return Detection{reachable[0], source, manager}, nil
}
func Reachable(ctx context.Context, port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	c, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	c.Close()
	return true
}
