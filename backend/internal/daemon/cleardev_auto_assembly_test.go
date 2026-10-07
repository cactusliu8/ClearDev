package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Keep the normal product wiring explicit: unit/manual fixtures may omit the
// setting, but the shipped daemon must not silently leave automatic execution off.
func TestDaemonEnablesApprovedPlanAutoAdvance(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "daemon.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	assemblies, enabled := 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		selector, ok := literal.Type.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Deps" {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "cleardevsvc" {
			return true
		}
		assemblies++
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			value, constant := pair.Value.(*ast.Ident)
			if ok && key.Name == "AutoAdvanceComplexPlans" && constant && value.Name == "true" {
				enabled++
			}
		}
		return true
	})
	if assemblies != 1 || enabled != 1 {
		t.Fatalf("normal ClearDev assemblies=%d automatically enabled=%d", assemblies, enabled)
	}
}
