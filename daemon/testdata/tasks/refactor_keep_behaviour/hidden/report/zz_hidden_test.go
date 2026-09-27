package report

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHiddenOutputIsUnchanged(t *testing.T) {
	cases := []struct{ got, want string }{
		{SalesTable([]Sale{{"North", 12, 340.5}, {"South-East", 3, 99}, {"W", 1200, 15000.25}}),
			"Region     | Units | Revenue \n-----------+-------+---------\nNorth      | 12    | 340.50  \nSouth-East | 3     | 99.00   \nW          | 1200  | 15000.25\n"},
		{StockTable([]Item{{"A-1", "Widget", 4, true}, {"LONG-SKU-22", "Gear", 120, false}}),
			"SKU         | Name   | On hand | Reorder\n------------+--------+---------+--------\nA-1         | Widget | 4       | yes    \nLONG-SKU-22 | Gear   | 120     |        \n"},
		{StaffTable([]Person{{"Ana", "cook", 30}, {"Bartholomew", "host", 12.5}}),
			"Name        | Role | Hours\n------------+------+------\nAna         | cook | 30.0 \nBartholomew | host | 12.5 \nTOTAL       |      | 42.5 \n"},
		{SalesTable(nil), "Region | Units | Revenue\n-------+-------+--------\n"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d changed:\n got %q\nwant %q", i, c.got, c.want)
		}
	}
}

func TestHiddenFormatTable(t *testing.T) {
	got := formatTable([]string{"A", "Bee"}, [][]string{{"long", "x"}, {"", "yz"}})
	want := "A    | Bee\n-----+----\nlong | x  \n     | yz \n"
	if got != want {
		t.Errorf("formatTable:\n got %q\nwant %q", got, want)
	}
}

// Each table function calls formatTable and lays nothing out itself.
func TestHiddenTheLayoutIsInOnePlace(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				funcs[fd.Name.Name] = fd
			}
		}
	}
	if funcs["formatTable"] == nil {
		t.Fatal("no formatTable function in the report package")
	}
	for _, name := range []string{"SalesTable", "StockTable", "StaffTable"} {
		fd := funcs[name]
		if fd == nil {
			t.Fatalf("%s is gone", name)
		}
		callsFormat, repeats := false, false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				callsFormat = callsFormat || fn.Name == "formatTable"
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok && x.Name == "strings" && fn.Sel.Name == "Repeat" {
					repeats = true
				}
			}
			return true
		})
		if !callsFormat {
			t.Errorf("%s does not call formatTable", name)
		}
		if repeats {
			t.Errorf("%s still lays out the table itself (it calls strings.Repeat)", name)
		}
	}
}
