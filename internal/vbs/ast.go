package vbs

// pos is a source position (1-based line and column).
type pos struct {
	line, col int
}

func (p pos) getPos() pos { return p }

// Expr is an expression node.
type Expr interface {
	getPos() pos
}

// Stmt is a statement node.
type Stmt interface {
	getPos() pos
}

type opKind uint8

const (
	opNone opKind = iota
	opAdd
	opSub
	opMul
	opDiv
	opIntDiv
	opMod
	opPow
	opConcat
	opEq
	opNe
	opLt
	opGt
	opLe
	opGe
	opIs
	opAnd
	opOr
	opXor
	opEqv
	opImp
	opNot
	opNeg
	opPos
)

// ---- expressions ----

// LitExpr is a literal: number, string, date, True/False, Empty, Null or
// Nothing. hard is set for number and string literals, which take part in
// VBScript's "hard type" comparison rules.
type LitExpr struct {
	pos
	val  Value
	hard bool
}

// Ident is a bare name: a variable, constant, or a procedure called without
// arguments.
type Ident struct {
	pos
	name  string
	lname string
}

// MeExpr is the Me keyword.
type MeExpr struct {
	pos
}

// MemberExpr is `obj.name`; obj is nil for the `.name` form inside With.
type MemberExpr struct {
	pos
	obj   Expr
	name  string
	lname string
}

// CallExpr is `target(args)`: a procedure call, an array subscript or a
// default-member access, decided at run time. It is also used for call
// statements, where the arguments may have been written without parentheses.
// Omitted arguments are nil.
type CallExpr struct {
	pos
	target Expr
	args   []Expr
}

// NewExpr is `New ClassName`.
type NewExpr struct {
	pos
	name  string
	lname string
}

// ParenExpr is a parenthesised expression; as an argument it forces ByVal.
type ParenExpr struct {
	pos
	x Expr
}

// UnaryExpr is a unary operator application.
type UnaryExpr struct {
	pos
	op opKind
	x  Expr
}

// BinaryExpr is a binary operator application.
type BinaryExpr struct {
	pos
	op           opKind
	l, r         Expr
	hardL, hardR bool // operand is a literal (comparison typing rules)
}

// ---- statements ----

// OptionStmt is `Option Explicit`.
type OptionStmt struct {
	pos
}

type dimDecl struct {
	pos
	name    string
	lname   string
	isArray bool
	dims    []Expr // upper bounds; empty for `Dim a()`
}

// DimStmt is Dim / Public / Private at module or procedure level.
type DimStmt struct {
	pos
	decls []dimDecl
}

type redimItem struct {
	target Expr // *Ident or *MemberExpr
	dims   []Expr
}

// ReDimStmt is `ReDim [Preserve] a(n), ...`.
type ReDimStmt struct {
	pos
	preserve bool
	items    []redimItem
}

type constDecl struct {
	pos
	name  string
	lname string
	val   Expr
}

// ConstStmt is `Const a = 1, b = 2`.
type ConstStmt struct {
	pos
	decls []constDecl
}

// AssignStmt is `target = value` or `Set target = value`.
type AssignStmt struct {
	pos
	target Expr
	val    Expr
	isSet  bool
}

// ExprStmt is a call statement.
type ExprStmt struct {
	pos
	x Expr
}

type ifBranch struct {
	cond Expr
	body []Stmt
}

// IfStmt covers both the block and the single-line form.
type IfStmt struct {
	pos
	branches []ifBranch
	els      []Stmt
	hasElse  bool
}

type caseClause struct {
	vals []Expr
	body []Stmt
}

// SelectStmt is `Select Case`.
type SelectStmt struct {
	pos
	expr    Expr
	cases   []caseClause
	els     []Stmt
	hasElse bool
}

// ForStmt is `For v = a To b [Step s] ... Next`.
type ForStmt struct {
	pos
	name     string
	lname    string
	from, to Expr
	step     Expr // nil when omitted
	body     []Stmt
}

// ForEachStmt is `For Each v In coll ... Next`.
type ForEachStmt struct {
	pos
	name  string
	lname string
	coll  Expr
	body  []Stmt
}

// DoStmt covers Do/Loop in all its forms and While/Wend.
type DoStmt struct {
	pos
	pre       Expr // condition tested before each iteration, or nil
	preUntil  bool
	post      Expr // condition tested after each iteration, or nil
	postUntil bool
	body      []Stmt
}

type ctrl uint8

const (
	ctrlNone ctrl = iota
	ctrlExitFor
	ctrlExitDo
	ctrlExitProc
)

// ExitStmt is `Exit For|Do|Sub|Function|Property`.
type ExitStmt struct {
	pos
	kind ctrl
}

// ProcStmt declares a Sub or Function.
type ProcStmt struct {
	pos
	proc *procDef
}

// ClassStmt declares a Class.
type ClassStmt struct {
	pos
	class *classDef
}

// WithStmt is `With obj ... End With`.
type WithStmt struct {
	pos
	obj  Expr
	body []Stmt
}

// OnErrorStmt is `On Error Resume Next` (resume) or `On Error GoTo 0`.
type OnErrorStmt struct {
	pos
	resume bool
}

// EraseStmt is `Erase a`.
type EraseStmt struct {
	pos
	target Expr
}

// ---- declarations ----

type procKind uint8

const (
	kindSub procKind = iota
	kindFunction
	kindPropGet
	kindPropLet
	kindPropSet
)

type param struct {
	name  string
	lname string
	byVal bool
}

// procDef is a Sub, Function or Property procedure.
type procDef struct {
	pos
	name      string
	lname     string
	kind      procKind
	params    []param
	body      []Stmt
	public    bool
	isDefault bool
	file      string
	explicit  bool // defined under Option Explicit

	decls     []Stmt // hoisted Dim/Const/ReDim declarations of the body
	declsDone bool
}

// returnsValue reports whether the procedure yields a value through its name.
func (p *procDef) returnsValue() bool {
	return p.kind == kindFunction || p.kind == kindPropGet
}

func (p *procDef) localDecls() []Stmt {
	if !p.declsDone {
		collectDecls(p.body, &p.decls)
		p.declsDone = true
	}
	return p.decls
}

type fieldDecl struct {
	dimDecl
	public bool
}

// classDef is a Class declaration.
type classDef struct {
	pos
	name       string
	lname      string
	fields     []fieldDecl
	fieldPub   map[string]bool
	constDecls []constDecl
	consts     map[string]Value
	methods    map[string]*procDef // Subs and Functions
	getters    map[string]*procDef
	letters    map[string]*procDef
	setters    map[string]*procDef
	defaultMem string // lower-cased name of the default member
	initProc   *procDef
	termProc   *procDef
	file       string
}

// collectDecls gathers the declarations that VBScript processes before a
// scope starts executing: they are found in nested blocks too, but not in
// nested procedures or classes.
func collectDecls(stmts []Stmt, out *[]Stmt) {
	for _, s := range stmts {
		switch x := s.(type) {
		case *DimStmt, *ConstStmt, *ReDimStmt, *ProcStmt, *ClassStmt, *OptionStmt:
			*out = append(*out, s)
		case *IfStmt:
			for _, br := range x.branches {
				collectDecls(br.body, out)
			}
			collectDecls(x.els, out)
		case *SelectStmt:
			for _, c := range x.cases {
				collectDecls(c.body, out)
			}
			collectDecls(x.els, out)
		case *ForStmt:
			collectDecls(x.body, out)
		case *ForEachStmt:
			collectDecls(x.body, out)
		case *DoStmt:
			collectDecls(x.body, out)
		case *WithStmt:
			collectDecls(x.body, out)
		}
	}
}
