//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"strconv"
	"strings"
)

const (
	integrationImport            = "dayorder.local/api/internal/agentintegration"
	httpAPIImport                = "dayorder.local/api/internal/httpapi"
	workerImport                 = "dayorder.local/api/internal/worker"
	agentHostImport              = "dayorder.local/api/internal/agenthost"
	integrationRouterConstructor = "NewAgentIntegrationRouter"
)

func workerConstructor(name string) bool {
	return name == "NewAgentHandler" || name == "NewReadonlyAgentHandler"
}

type sourceFile struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

func main() {
	var sources []sourceFile
	if err := json.NewDecoder(os.Stdin).Decode(&sources); err != nil {
		fmt.Fprintln(os.Stderr, "decode Go architecture sources")
		os.Exit(1)
	}
	failures := make([]string, 0)
	for _, source := range sources {
		failures = append(failures, inspectSource(source)...)
	}
	if err := json.NewEncoder(os.Stdout).Encode(failures); err != nil {
		fmt.Fprintln(os.Stderr, "encode Go architecture failures")
		os.Exit(1)
	}
}

func inspectSource(source sourceFile) []string {
	normalized := strings.ReplaceAll(source.Path, "\\", "/")
	if strings.HasPrefix(normalized, "apps/api/internal/agentintegration/") || strings.HasPrefix(normalized, "apps/api/cmd/agent-integration/") {
		return nil
	}
	fileset := token.NewFileSet()
	file, err := parser.ParseFile(fileset, source.Path, source.Source, parser.AllErrors)
	if err != nil {
		return []string{fmt.Sprintf("%s: Go source could not be parsed for Agent integration isolation", source.Path)}
	}
	integrationAliases := make(map[string]struct{})
	httpAPIAliases := make(map[string]struct{})
	workerAliases := make(map[string]struct{})
	agentHostAliases := make(map[string]struct{})
	dotIntegrationImport := false
	dotHTTPAPIImport := false
	dotWorkerImport := false
	dotAgentHostImport := false
	failures := make([]string, 0, 2)
	for _, spec := range file.Imports {
		importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
		if unquoteErr != nil || (importPath != integrationImport && importPath != httpAPIImport && importPath != workerImport && importPath != agentHostImport) {
			continue
		}
		alias := path.Base(importPath)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if importPath == integrationImport {
			failures = append(failures, fmt.Sprintf("%s: production Go package must not import internal/agentintegration", source.Path))
			if alias == "." {
				dotIntegrationImport = true
			} else if alias != "_" {
				integrationAliases[alias] = struct{}{}
			}
			continue
		}
		switch importPath {
		case httpAPIImport:
			if alias == "." {
				dotHTTPAPIImport = true
			} else if alias != "_" {
				httpAPIAliases[alias] = struct{}{}
			}
		case workerImport:
			if alias == "." {
				dotWorkerImport = true
			} else if alias != "_" {
				workerAliases[alias] = struct{}{}
			}
		case agentHostImport:
			if alias == "." {
				dotAgentHostImport = true
			} else if alias != "_" {
				agentHostAliases[alias] = struct{}{}
			}
		}
	}
	declaredConstructors := make(map[*ast.Ident]struct{})
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && (function.Name.Name == integrationRouterConstructor || workerConstructor(function.Name.Name) || function.Name.Name == "NewBackground") {
			declaredConstructors[function.Name] = struct{}{}
		}
	}
	sameHTTPAPIPackage := strings.HasPrefix(normalized, "apps/api/internal/httpapi/") && file.Name.Name == "httpapi"
	sameWorkerPackage := strings.HasPrefix(normalized, "apps/api/internal/worker/") && file.Name.Name == "worker"
	sameAgentHostPackage := strings.HasPrefix(normalized, "apps/api/internal/agenthost/") && file.Name.Name == "agenthost"
	integrationConstructor := false
	routerConstructorReference := false
	workerConstructorReferences := make(map[string]bool)
	agentHostConstructorReference := false
	selectorMembers := make(map[*ast.Ident]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.SelectorExpr:
			selectorMembers[value.Sel] = struct{}{}
			identifier, ok := value.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, imported := integrationAliases[identifier.Name]; imported && (strings.HasPrefix(value.Sel.Name, "New") || value.Sel.Name == "Host") {
				integrationConstructor = true
			}
			if _, imported := httpAPIAliases[identifier.Name]; imported && value.Sel.Name == integrationRouterConstructor {
				routerConstructorReference = true
			}
			if _, imported := workerAliases[identifier.Name]; imported && workerConstructor(value.Sel.Name) {
				workerConstructorReferences[value.Sel.Name] = true
			}
			if _, imported := agentHostAliases[identifier.Name]; imported && value.Sel.Name == "NewBackground" {
				agentHostConstructorReference = true
			}
		case *ast.Ident:
			if dotIntegrationImport && (strings.HasPrefix(value.Name, "New") || value.Name == "Host") {
				integrationConstructor = true
			}
			_, declaration := declaredConstructors[value]
			_, selectorMember := selectorMembers[value]
			if !declaration && value.Name == integrationRouterConstructor && (dotHTTPAPIImport || sameHTTPAPIPackage) {
				routerConstructorReference = true
			}
			if !declaration && !selectorMember && workerConstructor(value.Name) && (dotWorkerImport || sameWorkerPackage) {
				workerConstructorReferences[value.Name] = true
			}
			if !declaration && !selectorMember && value.Name == "NewBackground" && (dotAgentHostImport || sameAgentHostPackage) {
				agentHostConstructorReference = true
			}
		}
		return true
	})
	if integrationConstructor {
		failures = append(failures, fmt.Sprintf("%s: production Go package must not use an Agent integration Host constructor", source.Path))
	}
	if routerConstructorReference {
		failures = append(failures, fmt.Sprintf("%s: production Go package must not reference httpapi.%s", source.Path, integrationRouterConstructor))
	}
	for _, constructor := range []string{"NewAgentHandler", "NewReadonlyAgentHandler"} {
		if workerConstructorReferences[constructor] {
			failures = append(failures, fmt.Sprintf("%s: production Go package must not reference worker.%s", source.Path, constructor))
		}
	}
	if agentHostConstructorReference {
		failures = append(failures, fmt.Sprintf("%s: production Go package must not reference agenthost.NewBackground", source.Path))
	}
	return failures
}
