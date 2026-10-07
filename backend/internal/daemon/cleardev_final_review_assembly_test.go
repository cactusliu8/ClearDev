package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Legacy fixtures may intentionally omit the new contract. The normal product
// assembly must always provide its durable final-review store; this is not a
// user-settable switch that can bypass requirement completion.
func TestDaemonRequiresFinalReviewForNewComplexExecutions(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "daemon.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	bindings := 0
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
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, keyOK := pair.Key.(*ast.Ident)
			value, valueOK := pair.Value.(*ast.Ident)
			if keyOK && valueOK && key.Name == "RequirementFinalReviews" && value.Name == "store" {
				bindings++
			}
		}
		return true
	})
	if bindings != 1 {
		t.Fatalf("normal daemon final-review durable store bindings=%d, want exactly one", bindings)
	}
}
