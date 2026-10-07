package devhandoff

import (
	"strings"
	"testing"
)

func TestParseGoRelease(t *testing.T) {
	got, err := parseGoRelease("go version go1.25.7 linux/amd64")
	if err != nil || got != "1.25.7" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := parseGoRelease("go version go1.25.7rc1 linux/amd64"); err == nil {
		t.Fatal("prerelease was accepted")
	}
}

func TestCheckVersionsRejectsWrongGoAndNode(t *testing.T) {
	pin := Pin{Go: "1.25.7", Node: "22.23.1", NPM: "10.9.8", GolangciLint: "2.12.2", GoImage: "cleardev-go-validation:1.25.7"}
	err := checkVersions(pin, Versions{Go: "go version go1.27.0 linux/amd64"}, true, false)
	if err == nil || !strings.Contains(err.Error(), "1.27.0") || !strings.Contains(err.Error(), "1.25.7") {
		t.Fatalf("wrong go: %v", err)
	}
	err = checkVersions(pin, Versions{Node: "v22.23.0", NPM: "10.9.8"}, false, true)
	if err == nil || !strings.Contains(err.Error(), "22.23.0") || !strings.Contains(err.Error(), "22.23.1") {
		t.Fatalf("wrong node: %v", err)
	}
	err = checkVersions(pin, Versions{Node: "v22.23.1", NPM: "10.9.7"}, false, true)
	if err == nil || !strings.Contains(err.Error(), "10.9.7") || !strings.Contains(err.Error(), "10.9.8") {
		t.Fatalf("wrong npm: %v", err)
	}
	if err := checkVersions(pin, Versions{Go: "go version go1.25.7 linux/amd64", Node: "v22.23.1", NPM: "10.9.8"}, true, true); err != nil {
		t.Fatal(err)
	}
}

func TestParseNodeAndNPMReleaseRejectsLooseVersions(t *testing.T) {
	for _, version := range []string{"22", "22.23", "v22.23.1-rc1"} {
		if _, err := parseNodeRelease(version); err == nil {
			t.Fatalf("Node.js version %q was accepted", version)
		}
	}
	for _, version := range []string{"10", "10.9", "v10.9.8"} {
		if _, err := parseNPMRelease(version); err == nil {
			t.Fatalf("npm version %q was accepted", version)
		}
	}
}
