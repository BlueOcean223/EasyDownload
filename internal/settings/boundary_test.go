package settings

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var controlledConfigMutationMethods = map[string]bool{
	"Commit": true, "Update": true, "Reset": true, "Import": true, "Load": true,
	"UpdateRuntimeMetadata": true,
}

type configMutationCall struct {
	Method   string
	Function string
	Line     int
}

// This architecture check protects the single user-settings mutation boundary.
// app.updateRuntimeMetadataBestEffort is the only approved writer outside this
// package and can only call ConfigManager's typed runtime-metadata API.
func TestConfigManagerMutationBoundary(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate settings boundary test")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" || path == filepath.Join(repositoryRoot, "internal", "config") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(relative), "internal/settings/") {
			return nil
		}

		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return err
		}
		for _, call := range findConfigMutationCalls(fileSet, parsed) {
			if filepath.ToSlash(relative) == "app.go" && call.Function == "startup" && call.Method == "Load" {
				continue
			}
			if filepath.ToSlash(relative) == "app.go" && call.Function == "updateRuntimeMetadataBestEffort" && call.Method == "UpdateRuntimeMetadata" {
				continue
			}
			t.Errorf("direct ConfigManager.%s outside Settings boundary at %s:%d", call.Method, filepath.ToSlash(relative), call.Line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConfigMutationScannerDoesNotDependOnReceiverName(t *testing.T) {
	fileSet := token.NewFileSet()
	fixture := `package fixture
import configpkg "EasyDownload/internal/config"
func bypass(cm *configpkg.ConfigManager) error {
    return cm.Update(nil, func(*configpkg.Config) error { return nil })
}`
	parsed, err := parser.ParseFile(fileSet, "fixture.go", fixture, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := findConfigMutationCalls(fileSet, parsed)
	if len(calls) != 1 || calls[0].Method != "Update" || calls[0].Function != "bypass" {
		t.Fatalf("arbitrary receiver mutation was not detected: %+v", calls)
	}
}

func findConfigMutationCalls(fileSet *token.FileSet, parsed *ast.File) []configMutationCall {
	aliases := configPackageAliases(parsed)
	if len(aliases) == 0 {
		return nil
	}
	configManagerFields := map[string]bool{}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structure.Fields.List {
				if !isImportedConfigManagerType(field.Type, aliases) {
					continue
				}
				for _, name := range field.Names {
					configManagerFields[name.Name] = true
				}
			}
		}
	}
	var calls []configMutationCall
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		configManagerNames := map[string]bool{}
		if function.Type.Params != nil {
			for _, field := range function.Type.Params.List {
				if !isImportedConfigManagerType(field.Type, aliases) {
					continue
				}
				for _, name := range field.Names {
					configManagerNames[name.Name] = true
				}
			}
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if ok {
				for index, rhs := range assignment.Rhs {
					call, ok := rhs.(*ast.CallExpr)
					if !ok || !isConfigManagerConstructor(call.Fun, aliases) || index >= len(assignment.Lhs) {
						continue
					}
					if name, ok := assignment.Lhs[index].(*ast.Ident); ok {
						configManagerNames[name.Name] = true
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !controlledConfigMutationMethods[selector.Sel.Name] {
				return true
			}
			if !isKnownConfigManagerReceiver(selector.X, configManagerNames, configManagerFields) {
				return true
			}
			calls = append(calls, configMutationCall{
				Method: selector.Sel.Name, Function: function.Name.Name,
				Line: fileSet.Position(call.Pos()).Line,
			})
			return true
		})
	}
	return calls
}

func configPackageAliases(parsed *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, imported := range parsed.Imports {
		if strings.Trim(imported.Path.Value, `"`) == "EasyDownload/internal/config" {
			name := "config"
			if imported.Name != nil {
				name = imported.Name.Name
			}
			aliases[name] = true
		}
	}
	return aliases
}

func isImportedConfigManagerType(expression ast.Expr, aliases map[string]bool) bool {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return isImportedConfigManagerType(value.X, aliases)
	case *ast.SelectorExpr:
		packageName, ok := value.X.(*ast.Ident)
		return ok && aliases[packageName.Name] && value.Sel.Name == "ConfigManager"
	default:
		return false
	}
}

func isConfigManagerConstructor(expression ast.Expr, aliases map[string]bool) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "NewConfigManager" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && aliases[packageName.Name]
}

func isKnownConfigManagerReceiver(expression ast.Expr, names, fields map[string]bool) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return names[value.Name]
	case *ast.SelectorExpr:
		return fields[value.Sel.Name]
	default:
		return false
	}
}

func TestConfigManagerDoesNotExposeGenericUserSettingWriters(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate settings boundary test")
	}
	configSource := filepath.Join(filepath.Dir(currentFile), "..", "config", "config.go")
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, configSource, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"Set": true, "Replace": true, "SetDownloadDir": true}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || !forbidden[function.Name.Name] {
			continue
		}
		if len(function.Recv.List) > 0 && isConfigManagerType(function.Recv.List[0].Type) {
			t.Errorf("ConfigManager.%s reintroduces a generic user-setting mutation escape hatch", function.Name.Name)
		}
	}
}

func isConfigManagerType(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return isConfigManagerType(value.X)
	case *ast.Ident:
		return value.Name == "ConfigManager"
	default:
		return false
	}
}
