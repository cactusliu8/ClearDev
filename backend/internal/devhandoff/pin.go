package devhandoff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const defaultPinPath = "docs/cleardev/development/toolchain.json"

// Pin is the machine-readable development toolchain declaration.
type Pin struct {
	Go            string `json:"go"`
	Node          string `json:"node"`
	NPM           string `json:"npm"`
	GolangciLint  string `json:"golangciLint"`
	GoImage       string `json:"goImage"`
	GoImageDigest string `json:"goImageDigest"`
}

func loadPin(root string) (Pin, error) {
	raw, err := os.ReadFile(filepath.Join(root, defaultPinPath))
	if err != nil {
		return Pin{}, err
	}
	var pin Pin
	if err := json.Unmarshal(raw, &pin); err != nil {
		return Pin{}, err
	}
	if pin.Go == "" || pin.Node == "" || pin.NPM == "" || pin.GolangciLint == "" {
		return Pin{}, fmt.Errorf("toolchain pin is incomplete")
	}
	return pin, nil
}

func goHowTo(pin Pin) string {
	image := pin.GoImage
	if pin.GoImageDigest != "" {
		image = pin.GoImage + "@" + pin.GoImageDigest
	}
	return fmt.Sprintf("请使用固定镜像 %s，或阅读 docs/cleardev/development/toolchain.md。不要在错误的 Go 版本上运行 lint。", image)
}

func nodeHowTo(pin Pin) string {
	return fmt.Sprintf("请在当前 Linux 本地切换到 Node.js %s 和 npm %s，具体方法见 docs/cleardev/development/toolchain.md。", pin.Node, pin.NPM)
}

// Versions are the toolchain version strings observed on this machine.
type Versions struct {
	Go   string
	Node string
	NPM  string
}

func checkVersions(pin Pin, got Versions, requireGo, requireNode bool) error {
	var problems []string
	if requireGo {
		actual, err := parseGoRelease(got.Go)
		if err != nil {
			problems = append(problems, fmt.Sprintf("无法识别 Go 版本 %q。本仓库固定为 Go %s。%s", strings.TrimSpace(got.Go), pin.Go, goHowTo(pin)))
		} else if actual != pin.Go {
			problems = append(problems, fmt.Sprintf("Go 版本是 %s，本仓库固定为 %s。%s", actual, pin.Go, goHowTo(pin)))
		}
	}
	if requireNode {
		actual, err := parseNodeRelease(got.Node)
		if err != nil {
			problems = append(problems, fmt.Sprintf("无法识别 Node.js 版本 %q。本仓库固定为 Node.js %s。%s", strings.TrimSpace(got.Node), pin.Node, nodeHowTo(pin)))
		} else if actual != pin.Node {
			problems = append(problems, fmt.Sprintf("Node.js 版本是 %s，本仓库固定为 %s。%s", actual, pin.Node, nodeHowTo(pin)))
		}
		actualNPM, err := parseNPMRelease(got.NPM)
		if err != nil {
			problems = append(problems, fmt.Sprintf("无法识别 npm 版本 %q。本仓库固定为 npm %s。%s", strings.TrimSpace(got.NPM), pin.NPM, nodeHowTo(pin)))
		} else if actualNPM != pin.NPM {
			problems = append(problems, fmt.Sprintf("npm 版本是 %s，本仓库固定为 %s。%s", actualNPM, pin.NPM, nodeHowTo(pin)))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "\n"))
}

var goReleaseRe = regexp.MustCompile(`\bgo(\d+\.\d+(?:\.\d+)?)`)

func parseGoRelease(output string) (string, error) {
	loc := goReleaseRe.FindStringSubmatchIndex(output)
	if loc == nil {
		return "", fmt.Errorf("unparseable go version")
	}
	version := output[loc[2]:loc[3]]
	rest := ""
	if loc[1] < len(output) {
		rest = output[loc[1]:]
	}
	if rest != "" {
		next := rest[0]
		if next == '-' || next == '+' || (next >= 'A' && next <= 'Z') || (next >= 'a' && next <= 'z') {
			return "", fmt.Errorf("unparseable go version")
		}
	}
	if strings.Count(version, ".") == 1 {
		return version + ".0", nil
	}
	return version, nil
}

var nodeReleaseRe = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)
var npmReleaseRe = regexp.MustCompile(`^(\d+\.\d+\.\d+)$`)

func parseNodeRelease(output string) (string, error) {
	match := nodeReleaseRe.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return "", fmt.Errorf("unparseable node version")
	}
	return match[1], nil
}

func parseNPMRelease(output string) (string, error) {
	match := npmReleaseRe.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return "", fmt.Errorf("unparseable npm version")
	}
	return match[1], nil
}
