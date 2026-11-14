package facts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
)

// externalDependencies is advisory syntax evidence, never a verdict about a
// test or a reason to skip reproduction. Arguments and secret values are absent.
func externalDependencies(dir string, files []string) []string {
	var out []string
	for _, name := range files {
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			continue
		}
		imports := map[string]string{}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			alias := filepath.Base(path)
			if im.Name != nil {
				alias = im.Name.Name
			}
			imports[alias] = path
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path, method := imports[id.Name], sel.Sel.Name
			category := ""
			switch {
			case path == "time" && oneOf(method, "Now", "Since", "Until", "Sleep", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick"):
				category = "clock/timing"
			case (path == "net" && oneOf(method, "Dial", "DialTimeout", "Listen", "ListenPacket", "LookupHost", "LookupIP")) || (path == "net/http" && oneOf(method, "Get", "Post", "PostForm", "Head", "ListenAndServe")):
				category = "network"
			case path == "database/sql" && oneOf(method, "Open", "OpenDB"):
				category = "database"
			case path == "math/rand" || path == "math/rand/v2" || path == "crypto/rand":
				category = "randomness"
			}
			if category != "" {
				out = append(out, category+": "+path+"."+method)
			}
			return true
		})
	}
	return dedupeSort(out)
}
func oneOf(s string, candidates ...string) bool {
	for _, c := range candidates {
		if s == c {
			return true
		}
	}
	return false
}
