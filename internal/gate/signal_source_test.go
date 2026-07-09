package gate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestInterventionSignal_OnlyOwnerSourcesConstruct is a source-level
// regression guard for ADR-0011 point 4(e)(iv): "라벨·이슈 코멘트·
// repository_dispatch를 해제 신호로 읽는 구현은 금지". Proving the absence
// of a forbidden signal source is otherwise a "prove a negative" problem —
// this test makes it checkable by asserting, mechanically, that the only
// two exported package-level functions returning InterventionSignal are the
// two named constructors this package ships (PRReviewSubmittedByOwner,
// GlobalWorkflowDispatchByOwner). If a future change adds e.g. a
// FromLabel(...) or FromIssueComment(...) constructor — representing a
// forbidden signal source — this test fails immediately, before anyone has
// to reason about every call site that might use it.
func TestInterventionSignal_OnlyOwnerSourcesConstruct(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir() error = %v", err)
	}

	want := map[string]bool{
		"PRReviewSubmittedByOwner":      false,
		"GlobalWorkflowDispatchByOwner": false,
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil { // skip methods, only free functions construct values
					continue
				}
				if !returnsInterventionSignal(fn) {
					continue
				}
				name := fn.Name.Name
				if _, expected := want[name]; !expected {
					t.Errorf("unexpected exported function %q returns InterventionSignal — only PRReviewSubmittedByOwner and GlobalWorkflowDispatchByOwner are allowed to construct a signal (ADR-0011 point 4(e)(iv))", name)
					continue
				}
				want[name] = true
			}
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("expected constructor %q not found in package source", name)
		}
	}
}

func returnsInterventionSignal(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		ident, ok := field.Type.(*ast.Ident)
		if ok && ident.Name == "InterventionSignal" {
			return true
		}
	}
	return false
}
