package cleardevdemo

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const demoUpdateSettingsFileName = "update-settings.json"

type demoUpdateSettings struct {
	Enabled    bool   `json:"enabled"`
	Channel    string `json:"channel"`
	NightlyAck bool   `json:"nightlyAck"`
	Feature    any    `json:"feature"`
}

// AllocateLayout creates a new isolated directory tree. It fails if the chosen
// evidence directory already exists.
func AllocateLayout(parent string) (Layout, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	suffix, err := randomSuffix()
	if err != nil {
		return Layout{}, err
	}
	return prepareLayout(filepath.Join(parent, "cleardev-demo-"+suffix))
}

func allocateLayout(opts Options) (Layout, error) {
	if strings.TrimSpace(opts.ForcedRoot) != "" {
		return prepareLayout(opts.ForcedRoot)
	}
	return AllocateLayout(opts.ParentDir)
}

func prepareLayout(root string) (Layout, error) {
	layout := Layout{
		Root:        root,
		HomeDir:     filepath.Join(root, "home"),
		DataDir:     filepath.Join(root, "ao-data"),
		RunFile:     filepath.Join(root, "ao-data", "running.json"),
		RepoDir:     filepath.Join(root, "repo"),
		EvidenceDir: filepath.Join(root, "evidence"),
		AppDir:      filepath.Join(root, "final-app"),
		StartupFile: filepath.Join(root, "ao-data", "desktop-startup.json"),
		VNCAuthFile: filepath.Join(root, "home", ".vnc-passwd"),
	}
	if _, err := os.Stat(layout.EvidenceDir); err == nil {
		return Layout{}, fmt.Errorf("evidence directory already exists: %s", layout.EvidenceDir)
	}
	for _, dir := range []string{layout.HomeDir, layout.DataDir, layout.RepoDir, layout.EvidenceDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Layout{}, err
		}
	}
	if err := seedDemoUpdateSettings(layout); err != nil {
		return Layout{}, err
	}
	ports, err := freeDistinctPorts(4)
	if err != nil {
		return Layout{}, err
	}
	layout.Port, layout.DebugPort, layout.AppPort, layout.VNCPort = ports[0], ports[1], ports[2], ports[3]
	return layout, nil
}

func seedDemoUpdateSettings(layout Layout) error {
	settingsPath := filepath.Join(filepath.Dir(layout.RunFile), demoUpdateSettingsFileName)
	settings := demoUpdateSettings{Enabled: false, Channel: "latest", NightlyAck: false, Feature: nil}
	if err := writeJSON(settingsPath, settings); err != nil {
		return fmt.Errorf("write isolated update settings: %w", err)
	}
	if err := os.Chmod(settingsPath, 0o600); err != nil {
		return fmt.Errorf("set isolated update settings permissions: %w", err)
	}
	return nil
}

func randomSuffix() (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return 0, fmt.Errorf("listener address is not TCP")
	}
	port := addr.Port
	if err := l.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func freeDistinctPorts(count int) ([]int, error) {
	ports := make([]int, 0, count)
	seen := make(map[int]bool, count)
	for len(ports) < count {
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	return ports, nil
}
