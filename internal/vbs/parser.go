package vbs

// maxParseDepth bounds expression nesting so that pathological input cannot
// overflow the Go stack.
const maxParseDepth = 500

// reservedWords cannot be used as plain identifiers. Member names (after a
// dot) may be any word. A few VBScript keywords (Step, Property, Default,
// Explicit, Error, Resume, GoTo, Preserve, Get, Let, Erase) are contextual
// and deliberately absent.
var reservedWords = map[string]bool{
	"and": true, "byref": true, "byval": true, "call": true, "case": true,
	"class": true, "const": true, "dim": true, "do": true, "each": true,
	"else": true, "elseif": true, "empty": true, "end": true, "eqv": true,
	"exit": true, "false": true, "for": true, "function": true, "if": true,
	"imp": true, "in": true, "is": true, "loop": true, "me": true, "mod": true,
	"new": true, "next": true, "not": true, "nothing": true, "null": true,
	"on": true, "option": true, "or": true, "private": true, "public": true,
	"redim": true, "select": true, "set": true, "sub": true, "then": true,
	"to": true, "true": true, "until": true, "wend": true, "while": true,
	"with": true, "xor": true,
}

type parser struct {
	toks    []token
	p       int
	file    string
	depth   int
	inProc  bool
	inClass bool
	globals map[string]bool // names of global procedures and classes seen so far
}

// declareGlobal reports a redefinition of a global procedure or class.
func (p *parser) declareGlobal(t token) {
	if p.globals == nil {
		p.globals = map[string]bool{}
	}
	if p.globals[t.lower] {
		p.fail(t, 1041, "Name redefined: '"+t.text+"'")
	}
	p.globals[t.lower] = true
}

type syntaxPanic struct{ err *Error }

func (p *parser) fail(t token, num int, msg string) {
	if msg == "" {
		msg = errText(num)
	}
	panic(syntaxPanic{&Error{Number: num, Message: msg, File: p.file, Line: t.line, Col: t.col, Syntax: true}})
}

func (p *parser) recoverSyntax(err **Error) {
	if r := recover(); r != nil {
		sp, ok := r.(syntaxPanic)
		if !ok {
			panic(r)
		}
		*err = sp.err
	}
}

// parseProgram parses a whole script (or the argument of Execute).
func parseProgram(src, file string) (stmts []Stmt, err *Error) {
	toks, lerr := lexSource(src, file)
	if lerr != nil {
		return nil, lerr
	}
	p := &parser{toks: toks, file: file}
	defer p.recoverSyntax(&err)
	stmts = p.parseBlock()
	if t := p.peek(); t.kind != tEOF {
		switch {
		case isKw(t, "next"):
			p.fail(t, 1055, "")
		case isKw(t, "loop"):
			p.fail(t, 1038, "")
		}
		p.fail(t, 1024, "")
	}
	return stmts, nil
}

// parseExpression parses a single expression (the argument of Eval).
func parseExpression(src, file string) (e Expr, err *Error) {
	toks, lerr := lexSource(src, file)
	if lerr != nil {
		return nil, lerr
	}
	p := &parser{toks: toks, file: file}
	defer p.recoverSyntax(&err)
	for p.peek().kind == tNL {
		p.next()
	}
	e = p.parseExpr()
	for p.peek().kind == tNL {
		p.next()
	}
	if t := p.peek(); t.kind != tEOF {
		p.fail(t, 1025, "")
	}
	return e, nil
}

// ---- token helpers ----

func tpos(t token) pos { return pos{t.line, t.col} }

func (p *parser) peek() token { return p.toks[p.p] }

func (p *parser) peekAt(n int) token {
	i := p.p + n
	if i >= len(p.toks) {
		i = len(p.toks) - 1
	}
	return p.toks[i]
}

func (p *parser) next() token {
	t := p.toks[p.p]
	if t.kind != tEOF {
		p.p++
	}
	return t
}

func isKw(t token, kw string) bool {
	return t.kind == tIdent && !t.bracket && t.lower == kw
}

func isOp(t token, op string) bool {
	return t.kind == tOp && t.text == op
}

func isReserved(t token) bool {
	return t.kind == tIdent && !t.bracket && reservedWords[t.lower]
}

func (p *parser) acceptKw(kw string) bool {
	if isKw(p.peek(), kw) {
		p.next()
		return true
	}
	return false
}

func (p *parser) acceptOp(op string) bool {
	if isOp(p.peek(), op) {
		p.next()
		return true
	}
	return false
}

func (p *parser) expectKw(kw string, num int) token {
	t := p.peek()
	if !isKw(t, kw) {
		p.fail(t, num, "")
	}
	return p.next()
}

func (p *parser) expectOp(op string, num int) token {
	t := p.peek()
	if !isOp(t, op) {
		p.fail(t, num, "")
	}
	return p.next()
}

// ident consumes a non-reserved identifier.
func (p *parser) ident() token {
	t := p.peek()
	if t.kind != tIdent || isReserved(t) {
		p.fail(t, 1010, "")
	}
	return p.next()
}

// memberName consumes the name after a dot, which may be any word.
func (p *parser) memberName() token {
	t := p.peek()
	if t.kind != tIdent {
		p.fail(t, 1010, "")
	}
	return p.next()
}

// isBlockEnd reports whether t closes (or continues) a block statement.
func isBlockEnd(t token) bool {
	if t.kind != tIdent || t.bracket {
		return false
	}
	switch t.lower {
	case "end", "else", "elseif", "next", "loop", "wend", "case":
		return true
	}
	return false
}

// endOfStmt reports whether t terminates a statement: a line end, a colon,
// or a keyword that closes an enclosing (possibly single-line) construct.
func endOfStmt(t token) bool {
	switch t.kind {
	case tNL, tColon, tEOF:
		return true
	}
	return isBlockEnd(t)
}

// ---- statements ----

// parseBlock parses statements up to a block-closing keyword or EOF.
func (p *parser) parseBlock() []Stmt {
	var out []Stmt
	for {
		t := p.peek()
		switch {
		case t.kind == tNL || t.kind == tColon:
			p.next()
			continue
		case t.kind == tEOF || isBlockEnd(t):
			return out
		}
		out = append(out, p.parseStatement())
		if t = p.peek(); !endOfStmt(t) {
			p.fail(t, 1025, "")
		}
	}
}

func (p *parser) parseStatement() Stmt {
	t := p.peek()
	ps := tpos(t)
	if isOp(t, ".") {
		return p.parseAssignOrCall()
	}
	if t.kind != tIdent {
		p.fail(t, 1024, "")
	}
	if t.bracket {
		return p.parseAssignOrCall()
	}
	switch t.lower {
	case "option":
		p.next()
		if nt := p.next(); !isKw(nt, "explicit") {
			p.fail(nt, 1002, "")
		}
		return &OptionStmt{ps}
	case "dim":
		p.next()
		return p.parseDim(ps)
	case "public", "private":
		p.next()
		nt := p.peek()
		switch {
		case isKw(nt, "sub"):
			p.next()
			return &ProcStmt{ps, p.parseProc(kindSub, t.lower == "public", false, ps)}
		case isKw(nt, "function"):
			p.next()
			return &ProcStmt{ps, p.parseProc(kindFunction, t.lower == "public", false, ps)}
		case isKw(nt, "const"):
			p.next()
			return p.parseConst(ps)
		case isKw(nt, "default"):
			p.fail(nt, 1048, "")
		case isKw(nt, "property") && p.peekAt(1).kind == tIdent:
			p.fail(nt, 1048, "")
		}
		return p.parseDim(ps)
	case "redim":
		p.next()
		return p.parseReDim(ps)
	case "const":
		p.next()
		return p.parseConst(ps)
	case "set":
		p.next()
		return p.parseAssign(ps, true)
	case "let":
		if nt := p.peekAt(1); nt.kind == tIdent || isOp(nt, ".") {
			p.next()
			return p.parseAssign(ps, false)
		}
	case "if":
		return p.parseIf()
	case "select":
		return p.parseSelect()
	case "for":
		return p.parseFor()
	case "do":
		return p.parseDo()
	case "while":
		return p.parseWhile()
	case "exit":
		p.next()
		kt := p.next()
		st := &ExitStmt{pos: ps}
		switch {
		case isKw(kt, "for"):
			st.kind = ctrlExitFor
		case isKw(kt, "do"):
			st.kind = ctrlExitDo
		case isKw(kt, "sub"), isKw(kt, "function"), isKw(kt, "property"):
			st.kind = ctrlExitProc
		default:
			p.fail(kt, 1039, "")
		}
		return st
	case "sub":
		p.next()
		return &ProcStmt{ps, p.parseProc(kindSub, true, false, ps)}
	case "function":
		p.next()
		return &ProcStmt{ps, p.parseProc(kindFunction, true, false, ps)}
	case "class":
		p.next()
		return p.parseClass(ps)
	case "call":
		p.next()
		e := p.parsePostfix()
		switch e.(type) {
		case *Ident, *MemberExpr, *CallExpr:
		default:
			p.fail(t, 1002, "")
		}
		return &ExprStmt{ps, e}
	case "with":
		p.next()
		obj := p.parseExpr()
		body := p.parseBlock()
		p.expectKw("end", 1014)
		p.expectKw("with", 1029)
		return &WithStmt{ps, obj, body}
	case "on":
		p.next()
		if et := p.next(); !isKw(et, "error") {
			p.fail(et, 1002, "")
		}
		at := p.next()
		switch {
		case isKw(at, "resume"):
			p.expectKw("next", 1002)
			return &OnErrorStmt{ps, true}
		case isKw(at, "goto"):
			if zt := p.next(); zt.kind != tNumber {
				p.fail(zt, 1002, "")
			}
			return &OnErrorStmt{ps, false}
		}
		p.fail(at, 1002, "")
	case "erase":
		if nt := p.peekAt(1); nt.kind == tIdent && !isReserved(nt) {
			p.next()
			return &EraseStmt{ps, p.parseCallTarget()}
		}
	case "property":
		if nt := p.peekAt(1); !p.inClass && (isKw(nt, "get") || isKw(nt, "let") || isKw(nt, "set")) && p.peekAt(2).kind == tIdent {
			p.fail(t, 1048, "")
		}
	case "me":
	default:
		if reservedWords[t.lower] {
			p.fail(t, 1024, "")
		}
	}
	return p.parseAssignOrCall()
}

func (p *parser) parseDimDecl() dimDecl {
	name := p.ident()
	d := dimDecl{pos: tpos(name), name: name.text, lname: name.lower}
	if p.acceptOp("(") {
		d.isArray = true
		if !p.acceptOp(")") {
			for {
				d.dims = append(d.dims, p.parseExpr())
				if !p.acceptOp(",") {
					break
				}
			}
			p.expectOp(")", 1006)
		}
	}
	return d
}

func (p *parser) parseDim(ps pos) Stmt {
	st := &DimStmt{pos: ps}
	for {
		st.decls = append(st.decls, p.parseDimDecl())
		if !p.acceptOp(",") {
			break
		}
	}
	return st
}

func (p *parser) parseReDim(ps pos) Stmt {
	st := &ReDimStmt{pos: ps}
	if isKw(p.peek(), "preserve") && p.peekAt(1).kind == tIdent {
		p.next()
		st.preserve = true
	}
	for {
		var target Expr
		t := p.peek()
		switch {
		case isOp(t, "."):
			p.next()
			n := p.memberName()
			target = &MemberExpr{pos: tpos(n), name: n.text, lname: n.lower}
		case isKw(t, "me"):
			p.next()
			target = &MeExpr{tpos(t)}
		default:
			n := p.ident()
			target = &Ident{tpos(n), n.text, n.lower}
		}
		for isOp(p.peek(), ".") {
			p.next()
			n := p.memberName()
			target = &MemberExpr{pos: tpos(n), obj: target, name: n.text, lname: n.lower}
		}
		p.expectOp("(", 1005)
		var dims []Expr
		for {
			dims = append(dims, p.parseExpr())
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")", 1006)
		st.items = append(st.items, redimItem{target, dims})
		if !p.acceptOp(",") {
			break
		}
	}
	return st
}

func (p *parser) parseConst(ps pos) *ConstStmt {
	st := &ConstStmt{pos: ps}
	for {
		name := p.ident()
		p.expectOp("=", 1011)
		st.decls = append(st.decls, constDecl{tpos(name), name.text, name.lower, p.parseExpr()})
		if !p.acceptOp(",") {
			break
		}
	}
	return st
}

func (p *parser) parseAssign(ps pos, isSet bool) Stmt {
	target := p.parseCallTarget()
	p.expectOp("=", 1011)
	return &AssignStmt{ps, target, p.parseExpr(), isSet}
}

// parseCallTarget parses the head of an assignment or call statement: a
// name (or a With member, or Me) followed by member accesses and
// parenthesised argument lists.
func (p *parser) parseCallTarget() Expr {
	t := p.next()
	var e Expr
	switch {
	case isOp(t, "."):
		n := p.memberName()
		e = &MemberExpr{pos: tpos(n), name: n.text, lname: n.lower}
	case isKw(t, "me"):
		e = &MeExpr{tpos(t)}
	case t.kind == tIdent && !isReserved(t):
		e = &Ident{tpos(t), t.text, t.lower}
	default:
		p.fail(t, 1024, "")
	}
	for {
		t = p.peek()
		switch {
		case isOp(t, "("):
			p.next()
			e = &CallExpr{pos: e.getPos(), target: e, args: p.parseParenArgs()}
		case isOp(t, ".") && !t.space:
			p.next()
			n := p.memberName()
			e = &MemberExpr{pos: tpos(n), obj: e, name: n.text, lname: n.lower}
		default:
			return e
		}
	}
}

// parseAssignOrCall parses `target = expr` or a call statement in any of its
// spellings: `Foo`, `Foo a, b`, `Foo(a)`, `Foo (a), b`, `obj.Method a`.
func (p *parser) parseAssignOrCall() Stmt {
	start := p.peek()
	ps := tpos(start)
	target := p.parseCallTarget()
	t := p.peek()
	if isOp(t, "=") {
		p.next()
		return &AssignStmt{ps, target, p.parseExpr(), false}
	}
	if endOfStmt(t) {
		// `Foo(a)`: the parentheses belong to the argument, which is
		// therefore passed by value.
		if ce, ok := target.(*CallExpr); ok && len(ce.args) == 1 && ce.args[0] != nil {
			ce.args[0] = &ParenExpr{ce.args[0].getPos(), ce.args[0]}
		}
		return &ExprStmt{ps, target}
	}
	var args []Expr
	if ce, ok := target.(*CallExpr); ok {
		// `Foo (a) + 1, b` or `Foo (a), b`: what looked like an argument
		// list is a parenthesised expression starting the first argument.
		if len(ce.args) != 1 || ce.args[0] == nil {
			p.fail(t, 1025, "")
		}
		first := p.parseExprSeed(&ParenExpr{ce.args[0].getPos(), ce.args[0]})
		target = ce.target
		args = append(args, first)
		if p.acceptOp(",") {
			args = append(args, p.parseBareArgs()...)
		}
	} else {
		args = p.parseBareArgs()
	}
	return &ExprStmt{ps, &CallExpr{pos: target.getPos(), target: target, args: args}}
}

// parseBareArgs parses the comma separated arguments of a call statement
// written without parentheses. Omitted arguments yield nil entries.
func (p *parser) parseBareArgs() []Expr {
	var args []Expr
	for {
		if isOp(p.peek(), ",") {
			args = append(args, nil)
			p.next()
			continue
		}
		if endOfStmt(p.peek()) {
			args = append(args, nil)
			return args
		}
		args = append(args, p.parseExpr())
		if !p.acceptOp(",") {
			return args
		}
	}
}

// parseParenArgs parses an argument list after its opening parenthesis and
// consumes the closing one.
func (p *parser) parseParenArgs() []Expr {
	args := []Expr{}
	if p.acceptOp(")") {
		return args
	}
	for {
		if isOp(p.peek(), ",") {
			args = append(args, nil)
			p.next()
			continue
		}
		if isOp(p.peek(), ")") {
			args = append(args, nil)
			break
		}
		args = append(args, p.parseExpr())
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")", 1006)
	return args
}

func (p *parser) parseIf() Stmt {
	ift := p.next()
	st := &IfStmt{pos: tpos(ift)}
	cond := p.parseExpr()
	p.expectKw("then", 1017)
	if t := p.peek(); t.kind == tNL || t.kind == tEOF {
		// Block form.
		st.branches = append(st.branches, ifBranch{cond, p.parseBlock()})
		for {
			t := p.peek()
			switch {
			case isKw(t, "elseif"):
				p.next()
				c := p.parseExpr()
				p.expectKw("then", 1017)
				st.branches = append(st.branches, ifBranch{c, p.parseBlock()})
				continue
			case isKw(t, "else"):
				if st.hasElse {
					p.fail(t, 1002, "")
				}
				p.next()
				st.hasElse = true
				st.els = p.parseBlock()
				continue
			case isKw(t, "end"):
				p.next()
				p.expectKw("if", 1012)
				return st
			}
			p.fail(t, 1014, "")
		}
	}
	// Single-line form.
	st.branches = append(st.branches, ifBranch{cond, p.parseInline()})
	for isKw(p.peek(), "elseif") {
		p.next()
		c := p.parseExpr()
		p.expectKw("then", 1017)
		st.branches = append(st.branches, ifBranch{c, p.parseInline()})
	}
	if p.acceptKw("else") {
		st.hasElse = true
		st.els = p.parseInline()
	}
	if isKw(p.peek(), "end") && isKw(p.peekAt(1), "if") {
		p.next()
		p.next()
	}
	return st
}

// parseInline parses the colon separated statements of one arm of a
// single-line If.
func (p *parser) parseInline() []Stmt {
	var out []Stmt
	for {
		for p.peek().kind == tColon {
			p.next()
		}
		if endOfStmt(p.peek()) {
			return out
		}
		out = append(out, p.parseStatement())
		t := p.peek()
		if t.kind == tColon {
			continue
		}
		if !endOfStmt(t) {
			p.fail(t, 1025, "")
		}
		return out
	}
}

func (p *parser) parseSelect() Stmt {
	st := &SelectStmt{pos: tpos(p.next())}
	p.expectKw("case", 1021)
	st.expr = p.parseExpr()
	for {
		t := p.peek()
		switch {
		case t.kind == tNL || t.kind == tColon:
			p.next()
			continue
		case isKw(t, "case"):
			p.next()
			if p.acceptKw("else") {
				if st.hasElse {
					p.fail(t, 1002, "")
				}
				st.hasElse = true
				st.els = p.parseBlock()
				continue
			}
			var cc caseClause
			for {
				cc.vals = append(cc.vals, p.parseExpr())
				if !p.acceptOp(",") {
					break
				}
			}
			cc.body = p.parseBlock()
			st.cases = append(st.cases, cc)
			continue
		case isKw(t, "end"):
			p.next()
			p.expectKw("select", 1022)
			return st
		}
		if t.kind == tEOF {
			p.fail(t, 1014, "")
		}
		p.fail(t, 1021, "")
	}
}

func (p *parser) parseFor() Stmt {
	ft := p.next()
	ps := tpos(ft)
	if p.acceptKw("each") {
		name := p.ident()
		p.expectKw("in", 1046)
		coll := p.parseExpr()
		body := p.parseBlock()
		p.expectKw("next", 1020)
		p.optionalNextVar()
		return &ForEachStmt{ps, name.text, name.lower, coll, body}
	}
	nt := p.peek()
	if nt.kind != tIdent || isReserved(nt) {
		p.fail(nt, 1040, "")
	}
	name := p.next()
	p.expectOp("=", 1011)
	st := &ForStmt{pos: ps, name: name.text, lname: name.lower}
	st.from = p.parseExpr()
	p.expectKw("to", 1013)
	st.to = p.parseExpr()
	if p.acceptKw("step") {
		st.step = p.parseExpr()
	}
	st.body = p.parseBlock()
	p.expectKw("next", 1020)
	p.optionalNextVar()
	return st
}

// optionalNextVar consumes the loop variable that may follow Next.
func (p *parser) optionalNextVar() {
	if t := p.peek(); t.kind == tIdent && !isReserved(t) {
		p.next()
	}
}

func (p *parser) parseDo() Stmt {
	st := &DoStmt{pos: tpos(p.next())}
	switch {
	case p.acceptKw("while"):
		st.pre = p.parseExpr()
	case p.acceptKw("until"):
		st.pre = p.parseExpr()
		st.preUntil = true
	default:
		if t := p.peek(); !endOfStmt(t) {
			p.fail(t, 1028, "")
		}
	}
	st.body = p.parseBlock()
	p.expectKw("loop", 1019)
	switch {
	case p.acceptKw("while"):
		st.post = p.parseExpr()
	case p.acceptKw("until"):
		st.post = p.parseExpr()
		st.postUntil = true
	}
	return st
}

func (p *parser) parseWhile() Stmt {
	st := &DoStmt{pos: tpos(p.next())}
	st.pre = p.parseExpr()
	st.body = p.parseBlock()
	p.expectKw("wend", 1018)
	return st
}

// parseProc parses a Sub, Function or Property after its introducing
// keyword(s) and consumes the matching End.
func (p *parser) parseProc(kind procKind, public, isDefault bool, ps pos) *procDef {
	if p.inProc {
		p.fail(p.peek(), 1002, "")
	}
	name := p.ident()
	if !p.inClass {
		p.declareGlobal(name)
	}
	pd := &procDef{pos: ps, name: name.text, lname: name.lower, kind: kind, public: public, isDefault: isDefault, file: p.file}
	if p.acceptOp("(") {
		if !p.acceptOp(")") {
			for {
				var prm param
				if p.acceptKw("byval") {
					prm.byVal = true
				} else {
					p.acceptKw("byref")
				}
				n := p.ident()
				prm.name, prm.lname = n.text, n.lower
				if p.acceptOp("(") {
					p.expectOp(")", 1006)
				}
				pd.params = append(pd.params, prm)
				if !p.acceptOp(",") {
					break
				}
			}
			p.expectOp(")", 1006)
		}
	}
	if (kind == kindPropLet || kind == kindPropSet) && len(pd.params) == 0 {
		p.fail(name, 1054, "")
	}
	p.inProc = true
	pd.body = p.parseBlock()
	p.inProc = false
	p.expectKw("end", 1014)
	switch kind {
	case kindSub:
		p.expectKw("sub", 1016)
	case kindFunction:
		p.expectKw("function", 1015)
	default:
		p.expectKw("property", 1050)
	}
	return pd
}

func (p *parser) parseClass(ps pos) Stmt {
	if p.inProc || p.inClass {
		p.fail(p.peek(), 1002, "")
	}
	name := p.ident()
	p.declareGlobal(name)
	cd := &classDef{
		pos: ps, name: name.text, lname: name.lower, file: p.file,
		fieldPub: map[string]bool{},
		methods:  map[string]*procDef{},
		getters:  map[string]*procDef{},
		letters:  map[string]*procDef{},
		setters:  map[string]*procDef{},
	}
	p.inClass = true
	defer func() { p.inClass = false }()
	for {
		t := p.peek()
		if t.kind == tNL || t.kind == tColon {
			p.next()
			continue
		}
		if t.kind == tEOF {
			p.fail(t, 1014, "")
		}
		if isKw(t, "end") {
			p.next()
			p.expectKw("class", 1047)
			return &ClassStmt{ps, cd}
		}
		mps := tpos(t)
		public, isDefault, hasVis := true, false, false
		if isKw(t, "public") || isKw(t, "private") {
			hasVis = true
			public = t.lower == "public"
			p.next()
			if dt := p.peek(); isKw(dt, "default") {
				if !public {
					p.fail(dt, 1057, "")
				}
				p.next()
				isDefault = true
			}
			t = p.peek()
		}
		switch {
		case isKw(t, "sub"):
			p.next()
			p.addClassProc(cd, p.parseProc(kindSub, public, isDefault, mps), t)
		case isKw(t, "function"):
			p.next()
			p.addClassProc(cd, p.parseProc(kindFunction, public, isDefault, mps), t)
		case isKw(t, "property"):
			p.next()
			kt := p.next()
			var kind procKind
			switch {
			case isKw(kt, "get"):
				kind = kindPropGet
			case isKw(kt, "let"):
				kind = kindPropLet
			case isKw(kt, "set"):
				kind = kindPropSet
			default:
				p.fail(kt, 1049, "")
			}
			p.addClassProc(cd, p.parseProc(kind, public, isDefault, mps), t)
		case isKw(t, "const"):
			if isDefault {
				p.fail(t, 1056, "")
			}
			p.next()
			cd.constDecls = append(cd.constDecls, p.parseConst(mps).decls...)
		default:
			if isDefault {
				p.fail(t, 1056, "")
			}
			if isKw(t, "dim") {
				p.next()
			} else if !hasVis {
				p.fail(t, 1024, "")
			}
			for {
				d := p.parseDimDecl()
				if _, dup := cd.fieldPub[d.lname]; dup {
					p.fail(t, 1041, "Name redefined: '"+d.name+"'")
				}
				cd.fields = append(cd.fields, fieldDecl{d, public})
				cd.fieldPub[d.lname] = public
				if !p.acceptOp(",") {
					break
				}
			}
		}
		if et := p.peek(); et.kind != tNL && et.kind != tColon && et.kind != tEOF {
			p.fail(et, 1025, "")
		}
	}
}

func (p *parser) addClassProc(cd *classDef, pd *procDef, t token) {
	var m map[string]*procDef
	switch pd.kind {
	case kindPropGet:
		m = cd.getters
	case kindPropLet:
		m = cd.letters
	case kindPropSet:
		m = cd.setters
	default:
		m = cd.methods
	}
	if _, dup := m[pd.lname]; dup {
		p.fail(t, 1041, "Name redefined: '"+pd.name+"'")
	}
	m[pd.lname] = pd
	if pd.isDefault {
		if pd.kind == kindPropLet || pd.kind == kindPropSet {
			p.fail(t, 1058, "")
		}
		if cd.defaultMem != "" {
			p.fail(t, 1052, "")
		}
		cd.defaultMem = pd.lname
	}
	if pd.kind == kindSub {
		switch pd.lname {
		case "class_initialize":
			if len(pd.params) > 0 {
				p.fail(t, 1053, "")
			}
			cd.initProc = pd
		case "class_terminate":
			if len(pd.params) > 0 {
				p.fail(t, 1053, "")
			}
			cd.termProc = pd
		}
	}
}

// ---- expressions ----

func (p *parser) parseExpr() Expr { return p.parseExprSeed(nil) }

// parseExprSeed parses an expression. A non-nil seed is an already parsed
// primary operand with which the expression starts.
func (p *parser) parseExprSeed(seed Expr) Expr {
	p.depth++
	if p.depth > maxParseDepth {
		p.fail(p.peek(), 1001, "Expression too complex")
	}
	e := p.parseImp(seed)
	p.depth--
	return e
}

func (p *parser) parseImp(seed Expr) Expr {
	l := p.parseEqv(seed)
	for isKw(p.peek(), "imp") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opImp, l: l, r: p.parseEqv(nil)}
	}
	return l
}

func (p *parser) parseEqv(seed Expr) Expr {
	l := p.parseXor(seed)
	for isKw(p.peek(), "eqv") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opEqv, l: l, r: p.parseXor(nil)}
	}
	return l
}

func (p *parser) parseXor(seed Expr) Expr {
	l := p.parseOr(seed)
	for isKw(p.peek(), "xor") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opXor, l: l, r: p.parseOr(nil)}
	}
	return l
}

func (p *parser) parseOr(seed Expr) Expr {
	l := p.parseAnd(seed)
	for isKw(p.peek(), "or") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opOr, l: l, r: p.parseAnd(nil)}
	}
	return l
}

func (p *parser) parseAnd(seed Expr) Expr {
	l := p.parseNot(seed)
	for isKw(p.peek(), "and") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opAnd, l: l, r: p.parseNot(nil)}
	}
	return l
}

func (p *parser) parseNot(seed Expr) Expr {
	if seed == nil && isKw(p.peek(), "not") {
		t := p.next()
		p.depth++
		if p.depth > maxParseDepth {
			p.fail(t, 1001, "Expression too complex")
		}
		x := p.parseNot(nil)
		p.depth--
		return &UnaryExpr{tpos(t), opNot, x}
	}
	return p.parseCompare(seed)
}

func isHardLit(e Expr) bool {
	switch x := e.(type) {
	case *LitExpr:
		return x.hard
	case *UnaryExpr:
		if x.op == opNeg || x.op == opPos {
			return isHardLit(x.x)
		}
	}
	return false
}

func (p *parser) parseCompare(seed Expr) Expr {
	l := p.parseConcat(seed)
	for {
		t := p.peek()
		var op opKind
		switch {
		case isOp(t, "="):
			op = opEq
		case isOp(t, "<>"):
			op = opNe
		case isOp(t, "<"):
			op = opLt
		case isOp(t, ">"):
			op = opGt
		case isOp(t, "<="):
			op = opLe
		case isOp(t, ">="):
			op = opGe
		case isKw(t, "is"):
			op = opIs
		default:
			return l
		}
		p.next()
		r := p.parseConcat(nil)
		l = &BinaryExpr{pos: tpos(t), op: op, l: l, r: r, hardL: isHardLit(l), hardR: isHardLit(r)}
	}
}

func (p *parser) parseConcat(seed Expr) Expr {
	l := p.parseAdd(seed)
	for isOp(p.peek(), "&") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opConcat, l: l, r: p.parseAdd(nil)}
	}
	return l
}

func (p *parser) parseAdd(seed Expr) Expr {
	l := p.parseMod(seed)
	for {
		t := p.peek()
		var op opKind
		switch {
		case isOp(t, "+"):
			op = opAdd
		case isOp(t, "-"):
			op = opSub
		default:
			return l
		}
		p.next()
		l = &BinaryExpr{pos: tpos(t), op: op, l: l, r: p.parseMod(nil)}
	}
}

func (p *parser) parseMod(seed Expr) Expr {
	l := p.parseIntDiv(seed)
	for isKw(p.peek(), "mod") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opMod, l: l, r: p.parseIntDiv(nil)}
	}
	return l
}

func (p *parser) parseIntDiv(seed Expr) Expr {
	l := p.parseMul(seed)
	for isOp(p.peek(), "\\") {
		t := p.next()
		l = &BinaryExpr{pos: tpos(t), op: opIntDiv, l: l, r: p.parseMul(nil)}
	}
	return l
}

func (p *parser) parseMul(seed Expr) Expr {
	l := p.parseUnary(seed)
	for {
		t := p.peek()
		var op opKind
		switch {
		case isOp(t, "*"):
			op = opMul
		case isOp(t, "/"):
			op = opDiv
		default:
			return l
		}
		p.next()
		l = &BinaryExpr{pos: tpos(t), op: op, l: l, r: p.parseUnary(nil)}
	}
}

func (p *parser) parseUnary(seed Expr) Expr {
	if seed == nil {
		t := p.peek()
		var op opKind
		switch {
		case isOp(t, "-"):
			op = opNeg
		case isOp(t, "+"):
			op = opPos
		case isKw(t, "not"):
			op = opNot
		}
		if op != opNone {
			p.next()
			p.depth++
			if p.depth > maxParseDepth {
				p.fail(t, 1001, "Expression too complex")
			}
			x := p.parseUnary(nil)
			p.depth--
			return &UnaryExpr{tpos(t), op, x}
		}
	}
	return p.parsePow(seed)
}

func (p *parser) parsePow(seed Expr) Expr {
	l := seed
	if l == nil {
		l = p.parsePostfix()
	}
	for isOp(p.peek(), "^") {
		t := p.next()
		var r Expr
		if s := p.peek(); isOp(s, "-") || isOp(s, "+") {
			p.next()
			op := opNeg
			if s.text == "+" {
				op = opPos
			}
			r = &UnaryExpr{tpos(s), op, p.parsePostfix()}
		} else {
			r = p.parsePostfix()
		}
		l = &BinaryExpr{pos: tpos(t), op: opPow, l: l, r: r}
	}
	return l
}

func (p *parser) parsePostfix() Expr {
	e := p.parsePrimary()
	for {
		t := p.peek()
		switch {
		case isOp(t, "("):
			p.next()
			e = &CallExpr{pos: e.getPos(), target: e, args: p.parseParenArgs()}
		case isOp(t, "."):
			p.next()
			n := p.memberName()
			e = &MemberExpr{pos: tpos(n), obj: e, name: n.text, lname: n.lower}
		default:
			return e
		}
	}
}

func (p *parser) parsePrimary() Expr {
	t := p.next()
	ps := tpos(t)
	switch t.kind {
	case tNumber, tString:
		return &LitExpr{ps, t.val, true}
	case tDate:
		return &LitExpr{ps, t.val, false}
	case tOp:
		switch t.text {
		case "(":
			e := p.parseExpr()
			p.expectOp(")", 1006)
			return &ParenExpr{ps, e}
		case ".":
			n := p.memberName()
			return &MemberExpr{pos: tpos(n), name: n.text, lname: n.lower}
		}
	case tIdent:
		if t.bracket {
			return &Ident{ps, t.text, t.lower}
		}
		switch t.lower {
		case "true":
			return &LitExpr{ps, true, false}
		case "false":
			return &LitExpr{ps, false, false}
		case "empty":
			return &LitExpr{ps, nil, false}
		case "null":
			return &LitExpr{ps, Null, false}
		case "nothing":
			return &LitExpr{ps, Value(Nothing), false}
		case "me":
			return &MeExpr{ps}
		case "new":
			n := p.peek()
			if n.kind != tIdent {
				p.fail(n, 1010, "")
			}
			p.next()
			return &NewExpr{ps, n.text, n.lower}
		}
		if !reservedWords[t.lower] {
			return &Ident{ps, t.text, t.lower}
		}
	}
	p.fail(t, 1023, "")
	return nil
}
