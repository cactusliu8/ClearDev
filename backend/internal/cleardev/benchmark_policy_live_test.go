package cleardev

import (
	"path/filepath"
	"testing"
)

func TestBenchmarkLiveManifestSeparatesDevAndFormal(t *testing.T) {
	root := t.TempDir()
	m := validBenchmarkManifest(filepath.Join(root, "product"), root)
	m.SchemaVersion = 3
	m.Purpose = BenchmarkPurposeDevLive
	m.FacilityVersion = BenchmarkFacilityVersionV17
	m.PlanUnitID = "DEV-SENSOR-G3"
	m.CheckPrefix = "CHK-DEV"
	m.Scene = 99
	if err := ValidateBenchmarkBackendManifest(m); err != nil {
		t.Fatal(err)
	}
	wrong := m
	wrong.CheckPrefix = "CHK-LIB"
	if ValidateBenchmarkBackendManifest(wrong) == nil {
		t.Fatal("DEV accepted formal checks")
	}
	m.Purpose = BenchmarkPurposeFormal
	m.PlanUnitID = "R1-T1-G3"
	m.Scene = 1
	m.CheckPrefix = "CHK-LIB"
	if err := ValidateBenchmarkBackendManifest(m); err != nil {
		t.Fatal(err)
	}
	m.Scene = 99
	if ValidateBenchmarkBackendManifest(m) == nil {
		t.Fatal("FORMAL inherited unlimited DEV scenes")
	}
}
