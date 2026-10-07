package cleardev

import (
	"fmt"
	"regexp"
	"strings"
)

// BenchmarkPurposeDevLive and BenchmarkPurposeFormal distinguish live bindings
// from the historical fixture purpose. Neither is itself execution authority.
const (
	BenchmarkPurposeDevLive     = "DEV_LIVE"
	BenchmarkPurposeFormal      = "FORMAL"
	BenchmarkFacilityVersionV17 = "s12b-v17"
)

var benchmarkFormalUnitPattern = regexp.MustCompile(`^R[1-4]-(T1|T2|T2F|T3|Q)-(G3|G4)$`)

func validateBenchmarkPurpose(m BenchmarkBackendManifest) error {
	if m.Kind != BenchmarkBackendManifestKind {
		return fmt.Errorf("benchmark manifest header is invalid")
	}
	switch m.Purpose {
	case BenchmarkPurposeOffline:
		if m.SchemaVersion != 1 || m.FacilityVersion != BenchmarkFacilityVersionV1 || m.Scene > 2 {
			return fmt.Errorf("benchmark fixture header or scene is invalid")
		}
	case BenchmarkPurposeDevLive:
		if m.SchemaVersion != 3 || m.FacilityVersion != BenchmarkFacilityVersionV17 || m.CheckPrefix != "CHK-DEV" || (m.PlanUnitID != "DEV-SENSOR-"+m.Group && m.PlanUnitID != "DEV-SENSOR-"+m.Group+"-F") {
			return fmt.Errorf("benchmark DEV_LIVE binding is invalid")
		}
	case BenchmarkPurposeFormal:
		match := benchmarkFormalUnitPattern.FindStringSubmatch(m.PlanUnitID)
		if m.SchemaVersion != 3 || m.FacilityVersion != BenchmarkFacilityVersionV17 || m.Scene > 2 || len(match) != 3 || match[2] != m.Group {
			return fmt.Errorf("benchmark FORMAL binding is invalid")
		}
		prefix := "CHK-INV"
		switch match[1] {
		case "T1":
			prefix = "CHK-LIB"
		case "T3":
			prefix = "CHK-TKT"
		}
		if m.CheckPrefix != prefix {
			return fmt.Errorf("benchmark FORMAL check prefix is invalid")
		}
	default:
		return fmt.Errorf("benchmark manifest purpose is invalid: %s", strings.TrimSpace(m.Purpose))
	}
	return nil
}
