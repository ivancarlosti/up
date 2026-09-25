package services

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// syncedModels are the model types whose tables the protocol carries. A write to one
// of them is a change the peers are owed, so it has to be published.
var syncedModels = map[string]bool{
	"Monitor":             true,
	"MonitorGroup":        true,
	"MonitorGroupMember":  true,
	"MonitorTemplate":     true,
	"StatusPage":          true,
	"StatusPageMonitor":   true,
	"StatusPageGroupLink": true,
	"Notification":        true,
	"MonitorNotification": true,
}

// writeCalls are the GORM verbs that change a row.
var writeCalls = map[string]bool{
	"Create": true, "Update": true, "Updates": true, "Delete": true, "Save": true,
}

// publishesInInventory reports whether a name is a publishing helper.
func publishesInInventory(name string) bool {
	switch {
	case strings.HasPrefix(name, "Emit"),
		strings.HasPrefix(name, "publish"),
		strings.HasPrefix(name, "Publish"),
		strings.HasPrefix(name, "tombstone"),
		strings.HasPrefix(name, "Tombstone"):
		return true
	}
	// The status page selection lives in the payload, so this helper is what turns
	// "the selection moved" into a published version of the page.
	return name == "touchStatusPage"
}

// mentionsSyncedModel reports whether an expression names one of the synced models.
func mentionsSyncedModel(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "models" && syncedModels[sel.Sel.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// writesASyncedTable reports whether a body changes a row of a synced model.
func writesASyncedTable(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !writeCalls[sel.Sel.Name] {
			return true
		}
		// The receiver chain matters: `Model(&models.Monitor{}).Where(..).Updates(..)`
		// names the model several calls up, so the whole expression is inspected.
		if mentionsSyncedModel(call) {
			found = true
			return false
		}
		return true
	})
	return found
}

// publishesSomething reports whether a body calls a publishing helper.
func publishesSomething(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if publishesInInventory(fun.Sel.Name) {
				found = true
				return false
			}
		case *ast.Ident:
			if publishesInInventory(fun.Name) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// serviceSources lists this package's non-test files.
func serviceSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	files := map[string]*ast.File{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files[name] = file
	}
	if len(files) == 0 {
		t.Fatal("no source file was read: the guard would pass on an empty package")
	}
	return files
}

// TestEverySyncedWritePublishes is the write-path inventory guard.
//
// It walks this package's source and fails when a function writes a synchronised
// table without publishing that write, unless the function is on the allow-list below
// with a reason.
//
// The guard exists because two silent bugs already had exactly this shape: a write
// path that never emitted, and an entity missing from the list of what a node serves.
// Both were invisible in the tests and appeared only as data missing on a peer.
func TestEverySyncedWritePublishes(t *testing.T) {
	// Functions that may write a synced table without publishing, with the reason.
	// Every entry is a place the rule cannot protect, so the list stays as short as it
	// can honestly be.
	allowed := map[string]string{
		// The apply path. Publishing there is FORBIDDEN, not merely unnecessary: a
		// receiver that re-published what it applied would echo the change back and
		// forth for ever. Every one of these runs with WithApply(ctx).
		"writeMonitor":              "apply path (no echo)",
		"writeGroup":                "apply path (no echo)",
		"writeNotification":         "apply path (no echo)",
		"writeTemplate":             "apply path (no echo)",
		"writeStatusPage":           "apply path (no echo)",
		"writeStatusPageSelection":  "apply path (no echo)",
		"applyMonitor":              "apply path (no echo)",
		"applyGroup":                "apply path (no echo)",
		"applyNotification":         "apply path (no echo)",
		"applyTemplate":             "apply path (no echo)",
		"applyStatusPage":           "apply path (no echo)",
		"applyRelation":             "apply path (no echo)",
		"trackApplied":              "records the identity of an applied change",
		"recordConflict":            "records the merge outcome, not a synced table",
		"deleteGroupCascade":        "shared cascade; the caller publishes the tombstones",
		"deleteStatusPageSelection": "shared cascade; the caller publishes the tombstone",
		"deleteMonitorCascade":      "shared cascade; the caller publishes the tombstones",
		"removeRelation":            "apply path (no echo)",
		// Shared join-table writers. Their callers take a before/after snapshot and
		// publish exactly what moved (publishMonitor, publishGroup), which is what keeps
		// a membership with one writer per edit.
		"replaceMonitorGroupMembers":  "caller publishes the membership diff",
		"replaceMonitorGroups":        "caller publishes the membership diff",
		"replaceMonitorNotifications": "caller publishes the link diff",
		// The mirror of a manual domain expiration date. Its caller (Create and
		// Update) publishes every touched sibling through publishDomainSiblings, in
		// the same transaction, so the write is not invisible to a peer.
		"resolveDomainExpiresAt": "caller publishes the mirrored monitors",
		// The selection lives inside the page payload, so the caller is what turns it
		// into a version: touchStatusPage (the selection endpoint) or publishStatusPage
		// (the create, which writes the selection and the page as ONE version).
		"writeMonitorSelection": "caller publishes the page",
	}

	checked := 0
	for path, file := range serviceSources(t) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, ok := allowed[fn.Name.Name]; ok {
				continue
			}
			if !writesASyncedTable(fn.Body) {
				continue
			}
			checked++
			if !publishesSomething(fn.Body) {
				t.Errorf("%s: %s writes a synchronised table but never publishes it — a peer would never see the change",
					path, fn.Name.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no write path was inspected: the guard would pass on an empty package")
	}
	t.Logf("inspected %d write paths", checked)
}

// TestApplySwitchCoversEverySyncedEntity keeps the two lists that decide a change's
// fate in step: what a node SERVES (models.SyncedEntities) and what it can APPLY (the
// switch in applyEntity).
//
// A mismatch is silent in both directions: an entity served but not applied lands in
// the "unsupported entity" default branch, and one applied but not served is written
// to the outbox and never handed to a peer.
func TestApplySwitchCoversEverySyncedEntity(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sync_apply.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing sync_apply.go: %v", err)
	}

	served := map[string]bool{}
	for _, entity := range models.SyncedEntities() {
		served["Entity"+camel(entity)] = true
	}

	applied := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range clause.List {
			sel, ok := expr.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "models" && strings.HasPrefix(sel.Sel.Name, "Entity") {
				applied[sel.Sel.Name] = true
			}
		}
		return true
	})

	for name := range served {
		if !applied[name] {
			t.Errorf("models.%s is served to peers but has no apply case: the receiver would skip it as unsupported", name)
		}
	}
	for name := range applied {
		if !served[name] {
			t.Errorf("models.%s is applied but is not in models.SyncedEntities: it would never be served", name)
		}
	}
}

// camel turns "monitor_group_member" into "MonitorGroupMember", so a served entity
// can be compared with the constant a switch names.
func camel(value string) string {
	parts := strings.Split(value, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}
