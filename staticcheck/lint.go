// Package staticcheck contains a linter for Go source code.
package staticcheck // import "honnef.co/go/staticcheck"

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	htmltemplate "html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"honnef.co/go/lint"
)

var Funcs = []lint.Func{
	CheckRegexps,
	CheckTemplate,
	CheckTimeParse,
	CheckEncodingBinary,
	CheckTimeSleepConstant,
	CheckWaitgroupAdd,
	CheckWaitgroupCopy,
	CheckInfiniteEmptyLoop,
	CheckDeferInInfiniteLoop,
	CheckTestMainExit,
	CheckExec,
	CheckLoopEmptyDefault,
	CheckLhsRhsIdentical,
	CheckScopedBreak,
	CheckUnsafePrintf,
	CheckURLs,
	CheckEarlyDefer,
}

func CheckRegexps(f *lint.File) {
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !lint.IsPkgDot(call.Fun, "regexp", "MustCompile") &&
			!lint.IsPkgDot(call.Fun, "regexp", "Compile") {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		typ := f.Pkg.TypesInfo.Types[call.Args[0]]
		if typ.Value == nil {
			return true
		}
		if typ.Value.Kind() != constant.String {
			return true
		}
		s := constant.StringVal(typ.Value)
		_, err := regexp.Compile(s)
		if err != nil {
			f.Errorf(call.Args[0], 1, "%s", err)
		}
		return true
	}
	f.Walk(fn)
}

func CheckTemplate(f *lint.File) {
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Parse" {
			return true
		}
		var kind string
		typ := f.Pkg.TypesInfo.TypeOf(sel.X)
		if typ == nil {
			return true
		}
		switch typ.String() {
		case "*text/template.Template":
			kind = "text"
		case "*html/template.Template":
			kind = "html"
		default:
			return true
		}

		val := f.Pkg.TypesInfo.Types[call.Args[0]].Value
		if val == nil {
			return true
		}
		if val.Kind() != constant.String {
			return true
		}
		s := constant.StringVal(val)
		var err error
		switch kind {
		case "text":
			_, err = texttemplate.New("").Parse(s)
		case "html":
			_, err = htmltemplate.New("").Parse(s)
		}
		if err != nil {
			// TODO(dominikh): whitelist other parse errors, if any
			if strings.Contains(err.Error(), "unexpected") {
				f.Errorf(call.Args[0], 1, "%s", err)
			}
		}
		return true
	}
	f.Walk(fn)
}

func CheckTimeParse(f *lint.File) {
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !lint.IsPkgDot(call.Fun, "time", "Parse") {
			return true
		}
		if len(call.Args) != 2 {
			return true
		}
		typ := f.Pkg.TypesInfo.Types[call.Args[0]]
		if typ.Value == nil {
			return true
		}
		if typ.Value.Kind() != constant.String {
			return true
		}
		s := constant.StringVal(typ.Value)
		s = strings.Replace(s, "_", " ", -1)
		s = strings.Replace(s, "Z", "-", -1)
		_, err := time.Parse(s, s)
		if err != nil {
			f.Errorf(call.Args[0], 1, "%s", err)
		}
		return true
	}
	f.Walk(fn)
}

func CheckEncodingBinary(f *lint.File) {
	// TODO(dominikh): also check binary.Read
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !lint.IsPkgDot(call.Fun, "binary", "Write") {
			return true
		}
		if len(call.Args) != 3 {
			return true
		}
		typ := f.Pkg.TypesInfo.TypeOf(call.Args[2])
		if typ == nil {
			return true
		}
		dataType := typ.Underlying()
		if typ, ok := dataType.(*types.Pointer); ok {
			dataType = typ.Elem().Underlying()
		}
		if typ, ok := dataType.(interface {
			Elem() types.Type
		}); ok {
			if _, ok := typ.(*types.Pointer); !ok {
				dataType = typ.Elem()
			}
		}

		if validEncodingBinaryType(dataType) {
			return true
		}
		f.Errorf(call.Args[2], 1, "type %s cannot be used with binary.Write",
			f.Pkg.TypesInfo.TypeOf(call.Args[2]))
		return true
	}
	f.Walk(fn)
}

func validEncodingBinaryType(typ types.Type) bool {
	typ = typ.Underlying()
	switch typ := typ.(type) {
	case *types.Basic:
		switch typ.Kind() {
		case types.Uint8, types.Uint16, types.Uint32, types.Uint64,
			types.Int8, types.Int16, types.Int32, types.Int64,
			types.Float32, types.Float64, types.Complex64, types.Complex128, types.Invalid:
			return true
		}
		return false
	case *types.Struct:
		n := typ.NumFields()
		for i := 0; i < n; i++ {
			if !validEncodingBinaryType(typ.Field(i).Type()) {
				return false
			}
		}
		return true
	case *types.Array:
		return validEncodingBinaryType(typ.Elem())
	case *types.Interface:
		// we can't determine if it's a valid type or not
		return true
	}
	return false
}

func CheckTimeSleepConstant(f *lint.File) {
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !lint.IsPkgDot(call.Fun, "time", "Sleep") {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		n, err := strconv.Atoi(lit.Value)
		if err != nil {
			return true
		}
		if n == 0 || n > 120 {
			// time.Sleep(0) is a seldomly used pattern in concurrency
			// tests. >120 might be intentional. 120 was chosen
			// because the user could've meant 2 minutes.
			return true
		}
		recommendation := "time.Sleep(time.Nanosecond)"
		if n != 1 {
			recommendation = fmt.Sprintf("time.Sleep(%d * time.Nanosecond)", n)
		}
		f.Errorf(call.Args[0], 1, "sleeping for %d nanoseconds is probably a bug. Be explicit if it isn't: %s", n, recommendation)
		return true
	}
	f.Walk(fn)
}

func CheckWaitgroupAdd(f *lint.File) {
	fn := func(node ast.Node) bool {
		g, ok := node.(*ast.GoStmt)
		if !ok {
			return true
		}
		fun, ok := g.Call.Fun.(*ast.FuncLit)
		if !ok {
			return true
		}
		if len(fun.Body.List) == 0 {
			return true
		}
		stmt, ok := fun.Body.List[0].(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := stmt.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		fn, ok := f.Pkg.TypesInfo.ObjectOf(sel.Sel).(*types.Func)
		if !ok {
			return true
		}
		if fn.FullName() == "(*sync.WaitGroup).Add" {
			f.Errorf(sel, 1, "should call %s before starting the goroutine to avoid a race",
				f.Render(stmt))
		}
		return true
	}
	f.Walk(fn)
}

func CheckWaitgroupCopy(f *lint.File) {
	fn := func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncType)
		if !ok {
			return true
		}
		for _, arg := range fn.Params.List {
			typ := f.Pkg.TypesInfo.TypeOf(arg.Type)
			if typ != nil && typ.String() == "sync.WaitGroup" {
				f.Errorf(arg, 1, "should pass sync.WaitGroup by pointer")
			}
		}
		return true
	}
	f.Walk(fn)
}

func CheckInfiniteEmptyLoop(f *lint.File) {
	fn := func(node ast.Node) bool {
		loop, ok := node.(*ast.ForStmt)
		if !ok || len(loop.Body.List) != 0 || loop.Cond != nil || loop.Init != nil {
			return true
		}
		f.Errorf(loop, 1, "should not use an infinite empty loop. It will spin. Consider select{} instead.")
		return true
	}
	f.Walk(fn)
}

func CheckDeferInInfiniteLoop(f *lint.File) {
	fn := func(node ast.Node) bool {
		mightExit := false
		var defers []ast.Stmt
		loop, ok := node.(*ast.ForStmt)
		if !ok || loop.Cond != nil {
			return true
		}
		fn2 := func(node ast.Node) bool {
			switch stmt := node.(type) {
			case *ast.ReturnStmt:
				mightExit = true
			case *ast.BranchStmt:
				// TODO(dominikh): if this sees a break in a switch or
				// select, it doesn't check if it breaks the loop or
				// just the select/switch. This causes some false
				// negatives.
				if stmt.Tok == token.BREAK {
					mightExit = true
				}
			case *ast.DeferStmt:
				defers = append(defers, stmt)
			case *ast.FuncLit:
				// Don't look into function bodies
				return false
			}
			return true
		}
		ast.Inspect(loop.Body, fn2)
		if mightExit {
			return true
		}
		for _, stmt := range defers {
			f.Errorf(stmt, 1, "defers in this infinite loop will never run")
		}
		return true
	}
	f.Walk(fn)
}

func CheckTestMainExit(f *lint.File) {
	fn := func(node ast.Node) bool {
		if !IsTestMain(f, node) {
			return true
		}

		arg := f.Pkg.TypesInfo.ObjectOf(node.(*ast.FuncDecl).Type.Params.List[0].Names[0])
		callsRun := false
		fn2 := func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if arg != f.Pkg.TypesInfo.ObjectOf(ident) {
				return true
			}
			if sel.Sel.Name == "Run" {
				callsRun = true
				return false
			}
			return true
		}
		ast.Inspect(node.(*ast.FuncDecl).Body, fn2)

		callsExit := false
		fn3 := func(node ast.Node) bool {
			expr, ok := node.(ast.Expr)
			if !ok {
				return true
			}
			if lint.IsPkgDot(expr, "os", "Exit") {
				callsExit = true
				return false
			}
			return true
		}
		ast.Inspect(node.(*ast.FuncDecl).Body, fn3)
		if !callsExit && callsRun {
			f.Errorf(node, 0.9, "TestMain should call os.Exit to set exit code")
		}
		return true
	}
	f.Walk(fn)
}

func IsTestMain(f *lint.File, node ast.Node) bool {
	decl, ok := node.(*ast.FuncDecl)
	if !ok {
		return false
	}
	if decl.Name.Name != "TestMain" {
		return false
	}
	if len(decl.Type.Params.List) != 1 {
		return false
	}
	arg := decl.Type.Params.List[0]
	if len(arg.Names) != 1 {
		return false
	}
	typ := f.Pkg.TypesInfo.TypeOf(arg.Type)
	return typ != nil && typ.String() == "*testing.M"
}

func CheckExec(f *lint.File) {
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !lint.IsPkgDot(call.Fun, "exec", "Command") {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		typ := f.Pkg.TypesInfo.Types[call.Args[0]]
		if typ.Value == nil {
			return true
		}
		if typ.Value.Kind() != constant.String {
			return true
		}
		val := constant.StringVal(typ.Value)
		if !strings.Contains(val, " ") || strings.Contains(val, `\`) {
			return true
		}
		f.Errorf(call.Args[0], 0.9, "first argument to exec.Command looks like a shell command, but a program name or path are expected")
		return true
	}
	f.Walk(fn)
}

func CheckLoopEmptyDefault(f *lint.File) {
	fn := func(node ast.Node) bool {
		loop, ok := node.(*ast.ForStmt)
		if !ok || len(loop.Body.List) != 1 || loop.Cond != nil || loop.Init != nil {
			return true
		}
		sel, ok := loop.Body.List[0].(*ast.SelectStmt)
		if !ok {
			return true
		}
		for _, c := range sel.Body.List {
			if comm, ok := c.(*ast.CommClause); ok && comm.Comm == nil && len(comm.Body) == 0 {
				f.Errorf(comm, 1, "should not have an empty default case in a for+select loop. The loop will spin.")
			}
		}
		return true
	}
	f.Walk(fn)
}

func CheckLhsRhsIdentical(f *lint.File) {
	hasFnCall := func(expr ast.Expr) bool {
		hasCall := false
		fn := func(node ast.Node) bool {
			if _, ok := node.(*ast.CallExpr); ok {
				hasCall = true
				return false
			}
			return true
		}
		ast.Inspect(expr, fn)
		return hasCall
	}

	fn := func(node ast.Node) bool {
		op, ok := node.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		switch op.Op {
		case token.EQL, token.NEQ:
			if basic, ok := f.Pkg.TypesInfo.TypeOf(op.X).(*types.Basic); ok {
				if kind := basic.Kind(); kind == types.Float32 || kind == types.Float64 {
					// f == f and f != f might be used to check for NaN
					return true
				}
			}
		case token.SUB, token.QUO, token.AND, token.REM, token.OR, token.XOR, token.AND_NOT,
			token.LAND, token.LOR, token.LSS, token.GTR, token.LEQ, token.GEQ:
		default:
			// For some ops, such as + and *, it can make sense to
			// have identical operands
			return true
		}

		if f.Render(op.X) != f.Render(op.Y) {
			return true
		}
		confidence := 1.0
		if hasFnCall(op) {
			confidence = 0.9
		}
		f.Errorf(op, confidence, "identical expressions on the left and right side of the '%s' operator", op.Op)
		return true
	}
	f.Walk(fn)
}

func CheckScopedBreak(f *lint.File) {
	fn := func(node ast.Node) bool {
		loop, ok := node.(*ast.ForStmt)
		if !ok {
			return true
		}
		for _, stmt := range loop.Body.List {
			var blocks [][]ast.Stmt
			switch stmt := stmt.(type) {
			case *ast.SwitchStmt:
				for _, c := range stmt.Body.List {
					blocks = append(blocks, c.(*ast.CaseClause).Body)
				}
			case *ast.SelectStmt:
				for _, c := range stmt.Body.List {
					blocks = append(blocks, c.(*ast.CommClause).Body)
				}
			default:
				continue
			}

			for _, body := range blocks {
				if len(body) == 0 {
					continue
				}
				lasts := []ast.Stmt{body[len(body)-1]}
				// TODO(dh): unfold all levels of nested block
				// statements, not just a single level if statement
				if ifs, ok := lasts[0].(*ast.IfStmt); ok {
					if len(ifs.Body.List) == 0 {
						continue
					}
					lasts[0] = ifs.Body.List[len(ifs.Body.List)-1]

					if block, ok := ifs.Else.(*ast.BlockStmt); ok {
						if len(block.List) != 0 {
							lasts = append(lasts, block.List[len(block.List)-1])
						}
					}
				}
				for _, last := range lasts {
					branch, ok := last.(*ast.BranchStmt)
					if !ok || branch.Tok != token.BREAK || branch.Label != nil {
						continue
					}
					f.Errorf(branch, 1, "ineffective break statement. Did you mean to break out of the outer loop?")
				}
			}
		}
		return true
	}
	f.Walk(fn)
}

func CheckUnsafePrintf(f *lint.File) {
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !lint.IsPkgDot(call.Fun, "fmt", "Printf") &&
			!lint.IsPkgDot(call.Fun, "fmt", "Sprintf") &&
			!lint.IsPkgDot(call.Fun, "log", "Printf") {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		switch call.Args[0].(type) {
		case *ast.CallExpr, *ast.Ident:
		default:
			return true
		}
		f.Errorf(call.Args[0], 1, "printf-style function with dynamic first argument and no further arguments should use print-style function instead")
		return true
	}
	f.Walk(fn)
}

func CheckURLs(f *lint.File) {
	fn := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !lint.IsPkgDot(call.Fun, "url", "Parse") {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		typ := f.Pkg.TypesInfo.Types[call.Args[0]]
		if typ.Value == nil {
			return true
		}
		if typ.Value.Kind() != constant.String {
			return true
		}
		s := constant.StringVal(typ.Value)
		_, err := url.Parse(s)
		if err != nil {
			f.Errorf(call.Args[0], 1, "invalid argument to url.Parse: %s", err)
		}
		return true
	}
	f.Walk(fn)
}

func CheckEarlyDefer(f *lint.File) {
	fn := func(node ast.Node) bool {
		block, ok := node.(*ast.BlockStmt)
		if !ok {
			return true
		}
		if len(block.List) < 2 {
			return true
		}
		for i, stmt := range block.List {
			if i == len(block.List)-1 {
				break
			}
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok {
				continue
			}
			if len(assign.Rhs) != 1 {
				continue
			}
			if len(assign.Lhs) < 2 {
				continue
			}
			if lhs, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident); ok && lhs.Name == "_" {
				continue
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			sig, ok := f.Pkg.TypesInfo.TypeOf(call.Fun).(*types.Signature)
			if !ok {
				continue
			}
			if sig.Results().Len() < 2 {
				continue
			}
			last := sig.Results().At(sig.Results().Len() - 1)
			// FIXME(dh): check that it's error from universe, not
			// another type of the same name
			if last.Type().String() != "error" {
				continue
			}
			lhs, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				continue
			}
			def, ok := block.List[i+1].(*ast.DeferStmt)
			if !ok {
				continue
			}
			sel, ok := def.Call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				continue
			}
			if ident.Obj != lhs.Obj {
				continue
			}
			if sel.Sel.Name != "Close" {
				continue
			}
			f.Errorf(def, 1, "should check returned error before deferring %s", f.Render(def.Call))
		}
		return true
	}
	f.Walk(fn)
}
