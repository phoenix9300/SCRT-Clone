package vbs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxCallDepth bounds procedure nesting; exceeding it raises error 28 (Out
// of stack space) instead of overflowing the Go stack.
const maxCallDepth = 2000

// frame is the execution context of the global code or of one procedure
// activation.
type frame struct {
	vars       map[string]*Value
	consts     map[string]Value
	proc       *procDef
	me         *classInstance
	withs      []Value
	resumeNext bool // On Error Resume Next is active
	explicit   bool // Option Explicit applies
	global     bool
	file       string
}

// Interpreter executes VBScript source. It keeps its global state (variables,
// procedures, classes) between calls, so a host typically calls Run once and
// then Call for entry points such as `Sub Main`.
//
// An Interpreter is not safe for concurrent use; all calls, including calls
// made by host objects back into script objects, must happen on the goroutine
// that is executing Run or Call.
type Interpreter struct {
	globals   map[string]*Value
	gconsts   map[string]Value
	procs     map[string]*procDef
	classes   map[string]*classDef
	hostFuncs map[string]func(args []Value) (Value, error)
	factories map[string]func() (Object, error)

	out   io.Writer
	stdin *bufio.Reader

	gframe *frame
	frames []*frame
	depth  int

	ctx  context.Context
	done <-chan struct{}

	errObj *errObject

	rndSeed uint32
	rndLast float64

	scriptFile string
	scriptArgs []string
	exitCode   int

	// Class_Terminate bookkeeping, see gc.go.
	termList   []*classInstance
	liveTerm   int
	temps      []Value
	gcPending  bool
	collecting bool
}

// New creates an interpreter with the standard VBScript built-ins, the
// global Err and WScript objects, and the built-in CreateObject classes
// (Scripting.Dictionary, Scripting.FileSystemObject, VBScript.RegExp,
// WScript.Shell, WScript.Network). Output goes to os.Stdout until SetOutput
// is called.
func New() *Interpreter {
	in := &Interpreter{
		globals:   map[string]*Value{},
		gconsts:   map[string]Value{},
		procs:     map[string]*procDef{},
		classes:   map[string]*classDef{},
		hostFuncs: map[string]func(args []Value) (Value, error){},
		factories: map[string]func() (Object, error){},
		out:       os.Stdout,
		stdin:     bufio.NewReader(strings.NewReader("")),
		ctx:       context.Background(),
		rndSeed:   0x50000,
	}
	in.gframe = &frame{vars: in.globals, consts: in.gconsts, global: true}
	in.errObj = &errObject{}
	in.SetGlobal("Err", in.errObj)
	in.SetGlobal("WScript", &wscriptObject{in: in})
	in.registerBuiltinClasses()
	return in
}

// SetGlobal defines (or replaces) a global variable visible to scripts, for
// example the host object "crt". Names are case-insensitive.
func (in *Interpreter) SetGlobal(name string, v Value) {
	ln := strings.ToLower(name)
	v = normalize(v)
	if c, ok := in.globals[ln]; ok {
		*c = v
		return
	}
	c := new(Value)
	*c = v
	in.globals[ln] = c
}

// Global returns the current value of a global variable or constant.
func (in *Interpreter) Global(name string) (Value, bool) {
	ln := strings.ToLower(name)
	if c, ok := in.globals[ln]; ok {
		return *c, true
	}
	v, ok := in.gconsts[ln]
	return v, ok
}

// SetFunc defines or overrides a global function or sub callable from
// scripts (for example MsgBox or InputBox). Host functions take precedence
// over built-in functions of the same name, but not over procedures defined
// by the script itself.
//
// CreateObject is special: a host function registered under that name is
// consulted only for ProgIDs that are not registered with RegisterClass and
// are not built in.
func (in *Interpreter) SetFunc(name string, fn func(args []Value) (Value, error)) {
	ln := strings.ToLower(name)
	if fn == nil {
		delete(in.hostFuncs, ln)
		return
	}
	in.hostFuncs[ln] = fn
}

// SetOutput sets the writer used by WScript.Echo, WScript.StdOut and the
// default MsgBox implementation.
func (in *Interpreter) SetOutput(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	in.out = w
}

// SetInput sets the reader behind WScript.StdIn (empty by default).
func (in *Interpreter) SetInput(r io.Reader) {
	if r == nil {
		r = strings.NewReader("")
	}
	in.stdin = bufio.NewReader(r)
}

// SetArguments sets the values exposed through WScript.Arguments.
func (in *Interpreter) SetArguments(args ...string) {
	in.scriptArgs = append([]string(nil), args...)
}

// RegisterClass makes a class available to CreateObject (and to `New Name`
// when progID contains no dot). It overrides a built-in class registered
// under the same ProgID; a factory that returns an error can therefore be
// used to disable a built-in such as WScript.Shell.
func (in *Interpreter) RegisterClass(progID string, factory func() (Object, error)) {
	ln := strings.ToLower(progID)
	if factory == nil {
		delete(in.factories, ln)
		return
	}
	in.factories[ln] = factory
}

// ExitCode returns the code passed to the most recent WScript.Quit.
func (in *Interpreter) ExitCode() int { return in.exitCode }

// HasProc reports whether the script defined a global Sub or Function with
// the given name.
func (in *Interpreter) HasProc(name string) bool {
	_, ok := in.procs[strings.ToLower(name)]
	return ok
}

// enter installs ctx as the cancellation context and returns a function that
// restores the previous one.
func (in *Interpreter) enter(ctx context.Context) func() {
	if ctx == nil {
		ctx = context.Background()
	}
	pc, pd := in.ctx, in.done
	in.ctx, in.done = ctx, ctx.Done()
	return func() { in.ctx, in.done = pc, pd }
}

// checkCtx returns the context error once the running context is done.
func (in *Interpreter) checkCtx() error {
	if in.done != nil {
		select {
		case <-in.done:
			return in.ctx.Err()
		default:
		}
	}
	return nil
}

// finish converts the error that ended a top-level Run/Call into what the
// host should see.
func (in *Interpreter) finish(err error, file string) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		if e.File == "" {
			e.File = file
		}
		return e
	}
	if errors.Is(err, ErrQuit) {
		return ErrQuit
	}
	if cerr := in.ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

func internalError(r any, file string) *Error {
	return &Error{Number: 51, Message: fmt.Sprintf("Internal error: %v", r), File: file}
}

// Run parses src and executes its global code. filename is used in error
// messages and for WScript.ScriptName.
//
// Leading lines that start with '#' (host directives such as
// `# $language = "VBScript"`) are ignored. Procedures and classes defined by
// the script remain available to later Run and Call invocations.
//
// Run returns nil on success, a *Error for syntax and uncaught runtime
// errors, ctx.Err() when the context is cancelled (cancellation is checked at
// every statement and loop iteration and cannot be trapped by the script),
// and ErrQuit when the script called WScript.Quit.
func (in *Interpreter) Run(ctx context.Context, src, filename string) (err error) {
	prog, perr := parseProgram(src, filename)
	if perr != nil {
		return perr
	}
	restore := in.enter(ctx)
	defer restore()
	nframes, depth := len(in.frames), in.depth
	f := in.gframe
	savedFile, savedResume, savedExplicit := f.file, f.resumeNext, f.explicit
	defer func() {
		if r := recover(); r != nil {
			in.frames, in.depth = in.frames[:nframes], depth
			err = internalError(r, filename)
		}
		if nframes > 0 {
			f.file, f.resumeNext, f.explicit = savedFile, savedResume, savedExplicit
		}
	}()
	in.scriptFile = filename
	f.file, f.resumeNext, f.explicit = filename, false, false
	in.frames = append(in.frames, f)
	err = in.hoist(f, prog)
	if err == nil {
		_, err = in.execBlock(f, prog)
	}
	in.frames = in.frames[:nframes]
	if nframes == 0 {
		in.temps = in.temps[:0]
		in.collect()
	}
	return in.finish(err, filename)
}

// Call invokes a Sub or Function defined by the script (for example
// `Sub Main`) and returns the function result (nil for a Sub). Arguments are
// passed by value. Errors are reported as for Run.
func (in *Interpreter) Call(ctx context.Context, name string, args ...Value) (ret Value, err error) {
	p := in.procs[strings.ToLower(name)]
	if p == nil {
		return nil, newErr(35, name)
	}
	restore := in.enter(ctx)
	defer restore()
	nframes, depth := len(in.frames), in.depth
	defer func() {
		if r := recover(); r != nil {
			in.frames, in.depth = in.frames[:nframes], depth
			ret, err = nil, internalError(r, p.file)
		}
	}()
	cells := make([]*Value, len(args))
	for i, a := range args {
		c := new(Value)
		*c = normalize(a)
		cells[i] = c
	}
	ret, err = in.callProc(p, nil, cells)
	if err != nil {
		err = withDetail(err, p.name)
	}
	if nframes == 0 {
		in.temps = in.temps[:0]
		in.collect()
	}
	if a, ok := ret.(*Array); ok {
		a.temp = false
	}
	return ret, in.finish(err, p.file)
}

// Eval evaluates a single VBScript expression in the global scope.
func (in *Interpreter) Eval(ctx context.Context, expr string) (ret Value, err error) {
	e, perr := parseExpression(expr, "")
	if perr != nil {
		return nil, perr
	}
	restore := in.enter(ctx)
	defer restore()
	nframes, depth := len(in.frames), in.depth
	defer func() {
		if r := recover(); r != nil {
			in.frames, in.depth = in.frames[:nframes], depth
			ret, err = nil, internalError(r, "")
		}
	}()
	in.frames = append(in.frames, in.gframe)
	ret, err = in.evalExpr(in.gframe, e)
	in.frames = in.frames[:nframes]
	return ret, in.finish(err, "")
}

// Close runs the Class_Terminate handlers of all class instances that are
// still alive. Call it when the interpreter is no longer needed; it is not
// required for scripts that do not use Class_Terminate.
func (in *Interpreter) Close() {
	defer func() { _ = recover() }()
	restore := in.enter(context.Background())
	defer restore()
	for len(in.termList) > 0 {
		list := in.termList
		in.termList = nil
		for _, inst := range list {
			in.terminate(inst)
		}
	}
}

// curFrame returns the frame of the code that is currently executing.
func (in *Interpreter) curFrame() *frame {
	if n := len(in.frames); n > 0 {
		return in.frames[n-1]
	}
	return in.gframe
}

// hostErr converts an error returned by a host object or function into a
// script-visible error. Context cancellation and ErrQuit pass through
// untouched (and cannot be trapped by On Error Resume Next).
func (in *Interpreter) hostErr(err error) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		if e.Line != 0 {
			// Raised by script code running underneath the host call.
			return e
		}
		cp := *e
		return &cp
	}
	if errors.Is(err, ErrQuit) {
		return ErrQuit
	}
	if in.ctx.Err() != nil {
		return in.ctx.Err()
	}
	var ve *Error
	if errors.As(err, &ve) {
		cp := *ve
		cp.Message = err.Error()
		return &cp
	}
	return &Error{Number: HostErrorNumber, Message: err.Error()}
}

// callFunction calls a global function by name with already evaluated
// arguments, honouring the usual precedence: script procedure, host
// function, built-in.
func (in *Interpreter) callFunction(name string, args []Value) (Value, error) {
	ln := strings.ToLower(name)
	if p := in.procs[ln]; p != nil {
		return in.callProc(p, nil, valueCells(args))
	}
	if h := in.hostFuncs[ln]; h != nil {
		return in.callHost(h, args)
	}
	if b := builtins[ln]; b != nil {
		return in.callBuiltin(b, args)
	}
	return nil, newErr(35, name)
}

func (in *Interpreter) callHost(h func([]Value) (Value, error), args []Value) (Value, error) {
	in.noteEscape(args)
	v, err := h(args)
	if err != nil {
		return nil, in.hostErr(err)
	}
	return normalize(v), nil
}

func valueCells(args []Value) []*Value {
	cells := make([]*Value, len(args))
	for i := range args {
		c := new(Value)
		*c = args[i]
		cells[i] = c
	}
	return cells
}

// ---- declarations ----

// hoist processes the declarations of a block of code before it executes:
// Option Explicit, procedures, classes, Dim, Const and implicit ReDim
// declarations.
func (in *Interpreter) hoist(f *frame, stmts []Stmt) error {
	var decls []Stmt
	collectDecls(stmts, &decls)
	return in.declare(f, decls)
}

func (in *Interpreter) declare(f *frame, decls []Stmt) error {
	if len(decls) == 0 {
		return nil
	}
	for _, d := range decls {
		if _, ok := d.(*OptionStmt); ok {
			f.explicit = true
		}
	}
	for _, d := range decls {
		switch s := d.(type) {
		case *ProcStmt:
			s.proc.explicit = f.explicit
			in.procs[s.proc.lname] = s.proc
		case *ClassStmt:
			in.classes[s.class.lname] = s.class
			for _, m := range []map[string]*procDef{s.class.methods, s.class.getters, s.class.letters, s.class.setters} {
				for _, p := range m {
					p.explicit = f.explicit
				}
			}
		}
	}
	for _, d := range decls {
		switch s := d.(type) {
		case *ClassStmt:
			cd := s.class
			cd.consts = map[string]Value{}
			for _, c := range cd.constDecls {
				v, err := in.evalExpr(f, c.val)
				if err != nil {
					return in.located(err, f, c.pos)
				}
				cd.consts[c.lname] = v
			}
		case *DimStmt:
			for i := range s.decls {
				if err := in.declareVar(f, f.vars, &s.decls[i]); err != nil {
					return in.located(err, f, s.decls[i].pos)
				}
			}
		case *ConstStmt:
			for _, c := range s.decls {
				v, err := in.evalExpr(f, c.val)
				if err != nil {
					return in.located(err, f, c.pos)
				}
				if f.consts == nil {
					f.consts = map[string]Value{}
				}
				f.consts[c.lname] = v
			}
		case *ReDimStmt:
			// ReDim declares its variable when nothing else did.
			for _, it := range s.items {
				if id, ok := it.target.(*Ident); ok && in.lookupVarCell(f, id.lname) == nil {
					f.vars[id.lname] = new(Value)
				}
			}
		}
	}
	return nil
}

// declareVar creates the variable described by d in vars unless it exists.
func (in *Interpreter) declareVar(f *frame, vars map[string]*Value, d *dimDecl) error {
	if _, ok := vars[d.lname]; ok {
		return nil
	}
	c := new(Value)
	if d.isArray {
		if len(d.dims) == 0 {
			*c = &Array{}
		} else {
			dims, err := in.evalDims(f, d.dims)
			if err != nil {
				return err
			}
			arr, err := newArrayDims(dims)
			if err != nil {
				return err
			}
			arr.fixed = true
			*c = arr
		}
	}
	vars[d.lname] = c
	return nil
}

// evalDims evaluates array bounds and returns the element counts.
func (in *Interpreter) evalDims(f *frame, exprs []Expr) ([]int, error) {
	dims := make([]int, len(exprs))
	for i, e := range exprs {
		v, err := in.evalExpr(f, e)
		if err != nil {
			return nil, err
		}
		n, err := toInt(v)
		if err != nil {
			return nil, err
		}
		if n < -1 || n >= maxArrayElems {
			return nil, newErr(9, "")
		}
		dims[i] = int(n) + 1
	}
	return dims, nil
}

// located fills in the source position of a runtime error that does not have
// one yet.
func (in *Interpreter) located(err error, f *frame, p pos) error {
	if e, ok := err.(*Error); ok {
		if e.Line == 0 {
			e.Line, e.Col = p.line, p.col
		}
		if e.File == "" {
			e.File = f.file
		}
		return e
	}
	return err
}
