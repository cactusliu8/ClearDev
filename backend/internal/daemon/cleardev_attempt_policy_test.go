package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDaemonFreezesBoundedMailAttemptsForNewExecutions(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "daemon.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	ast.Inspect(file, func(n ast.Node) bool {
		literal, ok := n.(*ast.CompositeLit)
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
			key, ok := pair.Key.(*ast.Ident)
			value, constant := pair.Value.(*ast.Ident)
			if ok && key.Name == "BoundedMailAttempts" && constant && value.Name == "true" {
				enabled++
			}
		}
		return true
	})
	if enabled != 1 {
		t.Fatalf("normal daemon bounded mail policy enabled=%d", enabled)
	}
}

// The product must explicitly use a long observation window, rather than the
// legacy short default retained by small/manual service fixtures.
func TestDaemonUsesLongAgentObservationWindow(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "daemon.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	windows := 0
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typ, ok := literal.Type.(*ast.SelectorExpr)
		if !ok || typ.Sel.Name != "Deps" {
			return true
		}
		pkg, ok := typ.X.(*ast.Ident)
		if !ok || pkg.Name != "cleardevsvc" {
			return true
		}
		for _, entry := range literal.Elts {
			pair, ok := entry.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			if !ok || key.Name != "StepTimeout" {
				continue
			}
			duration, ok := pair.Value.(*ast.BinaryExpr)
			if !ok || duration.Op != token.MUL {
				t.Fatal("observation window is not explicit")
			}
			count, ok := duration.X.(*ast.BasicLit)
			if !ok || count.Value != "24" {
				t.Fatal("observation checkpoint is not 24 hours")
			}
			unit, ok := duration.Y.(*ast.SelectorExpr)
			if !ok || unit.Sel.Name != "Hour" {
				t.Fatal("observation window is still measured in minutes")
			}
			clock, ok := unit.X.(*ast.Ident)
			if !ok || clock.Name != "time" {
				t.Fatal("window does not use the standard clock")
			}
			windows++
		}
		return true
	})
	if windows != 1 {
		t.Fatalf("shipped observation windows=%d", windows)
	}
}
