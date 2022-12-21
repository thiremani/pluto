package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"strconv"
)

func emitExpr(expr ast.Expr) {
	switch e := expr.(type) {
	case *ast.CallExpr:
		arg0 := e.Args[0]
		fun := e.Fun
		fmt.Printf("# fun=%T\n", fun)
		switch fn := fun.(type) {
		case *ast.SelectorExpr:
			emitExpr(arg0)
			fmt.Printf("  popq %%rax\n")
			fmt.Printf("  pushq %%rax\n")
			symbol := fmt.Sprintf("%s.%s", fn.X, fn.Sel)
			fmt.Printf("  call %s\n", symbol)
		}
	case *ast.ParenExpr:
		emitExpr(e.X)
	case *ast.BasicLit:
		fmt.Printf("# start %T\n", e)
		val := e.Value
		ival, _ := strconv.Atoi(val)
		fmt.Printf("  movq $%d, %%rax\n", ival)
		fmt.Printf("  pushq %%rax\n")
		fmt.Printf("# end %T\n", e)
	case *ast.BinaryExpr:
		fmt.Printf("# start %T\n", e)
		emitExpr(e.X) // left
		emitExpr(e.Y) // right
		if e.Op.String() == "+" {
			fmt.Printf("  popq %%rdi # right\n")
			fmt.Printf("  popq %%rax # left\n")
			fmt.Printf("  addq %%rdi, %%rax\n")
			fmt.Printf("  pushq %%rax\n")
		} else if e.Op.String() == "-" {
			fmt.Printf("  popq %%rdi # right\n")
			fmt.Printf("  popq %%rax # left\n")
			fmt.Printf("  subq %%rdi, %%rax\n")
			fmt.Printf("  pushq %%rax\n")
		} else if e.Op.String() == "*" {
			fmt.Printf("  popq %%rdi # right\n")
			fmt.Printf("  popq %%rax # left\n")
			fmt.Printf("  imulq %%rdi, %%rax\n")
			fmt.Printf("  pushq %%rax\n")
		} else {
			panic(fmt.Sprintf("Unexpected binary operator %s", e.Op))
		}
		fmt.Printf("# end %T\n", e)
	default:
		panic(fmt.Sprintf("Unexpected expr type %T", expr))
	}
}

func main() {
	source := "os.Exit((20 + 1) * 2)"
	expr, err := parser.ParseExpr(source)
	if err != nil {
		panic(err)
	}
	fmt.Printf(".text\n")
	fmt.Printf(".global main.main\n")
	fmt.Printf("main.main:\n")
	emitExpr(expr)
	fmt.Printf("  ret\n")
}
