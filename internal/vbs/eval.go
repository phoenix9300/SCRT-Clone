package vbs

// locate fills in the position of an unlocated runtime error.
func locate(err error, p pos) error {
	if e, ok := err.(*Error); ok && e.Line == 0 {
		e.Line, e.Col = p.line, p.col
	}
	return err
}

// exprName returns a printable name for an expression used in error text.
func exprName(e Expr) string {
	switch x := e.(type) {
	case *Ident:
		return x.name
	case *MemberExpr:
		return x.name
	case *CallExpr:
		return exprName(x.target)
	case *ParenExpr:
		return exprName(x.x)
	}
	return ""
}

func (in *Interpreter) evalExpr(f *frame, e Expr) (Value, error) {
	switch e := e.(type) {
	case *LitExpr:
		return e.val, nil
	case *Ident:
		v, err := in.evalIdent(f, e)
		if err != nil {
			return nil, locate(err, e.pos)
		}
		return v, nil
	case *BinaryExpr:
		l, err := in.evalExpr(f, e.l)
		if err != nil {
			return nil, err
		}
		r, err := in.evalExpr(f, e.r)
		if err != nil {
			return nil, err
		}
		v, err := binaryOp(e.op, l, r, e.hardL, e.hardR)
		if err != nil {
			return nil, locate(err, e.pos)
		}
		return v, nil
	case *UnaryExpr:
		x, err := in.evalExpr(f, e.x)
		if err != nil {
			return nil, err
		}
		v, err := unaryOp(e.op, x)
		if err != nil {
			return nil, locate(err, e.pos)
		}
		return v, nil
	case *CallExpr:
		v, err := in.evalCall(f, e)
		if err != nil {
			return nil, locate(err, e.pos)
		}
		return v, nil
	case *MemberExpr:
		v, err := in.evalMember(f, e, nil)
		if err != nil {
			return nil, locate(err, e.pos)
		}
		return v, nil
	case *ParenExpr:
		return in.evalExpr(f, e.x)
	case *MeExpr:
		if f.me == nil {
			return nil, locate(newErr(424, "Me"), e.pos)
		}
		return f.me, nil
	case *NewExpr:
		v, err := in.evalNew(e)
		if err != nil {
			return nil, locate(err, e.pos)
		}
		return v, nil
	}
	return nil, newErrMsg(51, "Internal error: unknown expression")
}

// ---- name resolution ----

type nameKind uint8

const (
	nkNone    nameKind = iota
	nkVar              // variable: cell
	nkConst            // constant: val
	nkProc             // global script procedure
	nkMember           // method or property getter of the current instance
	nkHost             // host function
	nkBuiltin          // built-in function
)

type resolved struct {
	kind nameKind
	cell *Value
	val  Value
	proc *procDef
	host func([]Value) (Value, error)
	bi   *builtin
}

// resolve looks a bare name up: locals, members of Me, globals, script
// procedures, host functions, built-in constants and functions.
func (in *Interpreter) resolve(f *frame, ln string) resolved {
	if c, ok := f.vars[ln]; ok {
		return resolved{kind: nkVar, cell: c}
	}
	if v, ok := f.consts[ln]; ok {
		return resolved{kind: nkConst, val: v}
	}
	if f.me != nil {
		if c, ok := f.me.fields[ln]; ok {
			return resolved{kind: nkVar, cell: c}
		}
		cl := f.me.class
		if v, ok := cl.consts[ln]; ok {
			return resolved{kind: nkConst, val: v}
		}
		if p := cl.methods[ln]; p != nil {
			return resolved{kind: nkMember, proc: p}
		}
		if p := cl.getters[ln]; p != nil {
			return resolved{kind: nkMember, proc: p}
		}
	}
	if !f.global {
		if c, ok := in.globals[ln]; ok {
			return resolved{kind: nkVar, cell: c}
		}
		if v, ok := in.gconsts[ln]; ok {
			return resolved{kind: nkConst, val: v}
		}
	}
	if p := in.procs[ln]; p != nil {
		return resolved{kind: nkProc, proc: p}
	}
	if h := in.hostFuncs[ln]; h != nil && ln != "createobject" {
		return resolved{kind: nkHost, host: h}
	}
	if v, ok := builtinConsts[ln]; ok {
		return resolved{kind: nkConst, val: v}
	}
	if b := builtins[ln]; b != nil {
		return resolved{kind: nkBuiltin, bi: b}
	}
	return resolved{}
}

// lookupVarCell returns the cell of an existing variable (local, field of
// Me, or global), or nil.
func (in *Interpreter) lookupVarCell(f *frame, ln string) *Value {
	if c, ok := f.vars[ln]; ok {
		return c
	}
	if f.me != nil {
		if c, ok := f.me.fields[ln]; ok {
			return c
		}
	}
	if !f.global {
		if c, ok := in.globals[ln]; ok {
			return c
		}
	}
	return nil
}

// varCell returns the cell of a variable, creating it when the name is
// unknown and implicit declaration is allowed (no Option Explicit, or
// declare is set).
func (in *Interpreter) varCell(f *frame, name, ln string, declare bool) (*Value, error) {
	if c := in.lookupVarCell(f, ln); c != nil {
		return c, nil
	}
	if r := in.resolve(f, ln); r.kind != nkNone && r.kind != nkBuiltin {
		return nil, newErr(501, name)
	}
	if f.explicit && !declare {
		return nil, newErr(500, name)
	}
	c := new(Value)
	f.vars[ln] = c
	return c, nil
}

// setCell stores v in a variable cell, applying array copy semantics and
// noticing dropped references to instances that have a Class_Terminate.
func (in *Interpreter) setCell(c *Value, v Value) {
	old := *c
	*c = storeValue(v)
	if in.liveTerm > 0 {
		if inst, ok := old.(*classInstance); ok && inst.class.termProc != nil && !inst.terminated {
			in.gcPending = true
		}
	}
}

func (in *Interpreter) evalIdent(f *frame, id *Ident) (Value, error) {
	r := in.resolve(f, id.lname)
	switch r.kind {
	case nkVar:
		return *r.cell, nil
	case nkConst:
		return r.val, nil
	case nkProc:
		v, err := in.callProc(r.proc, nil, nil)
		return v, withDetail(err, id.name)
	case nkMember:
		v, err := in.callProc(r.proc, f.me, nil)
		return v, withDetail(err, id.name)
	case nkHost:
		return in.callHost(r.host, nil)
	case nkBuiltin:
		return in.callBuiltin(r.bi, nil)
	}
	if f.explicit {
		return nil, newErr(500, id.name)
	}
	f.vars[id.lname] = new(Value)
	return nil, nil
}

func (in *Interpreter) evalNew(e *NewExpr) (Value, error) {
	if cd := in.classes[e.lname]; cd != nil {
		return in.newInstance(cd)
	}
	if o, found, err := in.createByName(e.lname); found {
		return o, err
	}
	return nil, newErr(506, e.name)
}

// ---- calls ----

func (in *Interpreter) evalCall(f *frame, ce *CallExpr) (Value, error) {
	switch t := ce.target.(type) {
	case *Ident:
		ln := t.lname
		if p := f.proc; p != nil && p.returnsValue() && p.lname == ln {
			// Inside a function its own name followed by an argument
			// list is a recursive call, not the return variable.
			return in.invokeProc(f, p, f.me, ce.args)
		}
		r := in.resolve(f, ln)
		switch r.kind {
		case nkVar:
			return in.indexInto(f, *r.cell, ce.args, t.name)
		case nkConst:
			return in.indexInto(f, r.val, ce.args, t.name)
		case nkProc:
			return in.invokeProc(f, r.proc, nil, ce.args)
		case nkMember:
			return in.invokeProc(f, r.proc, f.me, ce.args)
		case nkHost:
			vals, err := in.evalArgs(f, ce.args)
			if err != nil {
				return nil, err
			}
			return in.callHost(r.host, vals)
		case nkBuiltin:
			vals, err := in.evalArgs(f, ce.args)
			if err != nil {
				return nil, err
			}
			return in.callBuiltin(r.bi, vals)
		}
		if f.explicit {
			return nil, newErr(500, t.name)
		}
		return nil, newErr(13, t.name)
	case *MemberExpr:
		return in.evalMember(f, t, ce.args)
	}
	v, err := in.evalExpr(f, ce.target)
	if err != nil {
		return nil, err
	}
	return in.indexInto(f, v, ce.args, "")
}

// invokeProc calls a script procedure with argument expressions, which lets
// ByRef parameters alias the caller's variables.
func (in *Interpreter) invokeProc(f *frame, p *procDef, me *classInstance, args []Expr) (Value, error) {
	mark := len(in.temps)
	cells, err := in.argCells(f, args)
	if err != nil {
		return nil, err
	}
	v, err := in.callProcFlex(p, me, cells)
	in.temps = in.temps[:mark]
	return v, withDetail(err, p.name)
}

// callProcFlex calls p; when arguments are supplied to a parameterless
// function or property, they index its result (`obj.Items(0)`).
func (in *Interpreter) callProcFlex(p *procDef, me *classInstance, cells []*Value) (Value, error) {
	if len(cells) > 0 && len(p.params) == 0 && p.returnsValue() {
		v, err := in.callProc(p, me, nil)
		if err != nil {
			return nil, err
		}
		vals := make([]Value, len(cells))
		for i, c := range cells {
			vals[i] = *c
		}
		return in.indexValues(v, vals)
	}
	return in.callProc(p, me, cells)
}

// callProc runs a script procedure. args are the argument cells: ByRef
// parameters use them directly, ByVal parameters copy their contents.
func (in *Interpreter) callProc(p *procDef, me *classInstance, args []*Value) (Value, error) {
	if len(args) != len(p.params) {
		return nil, newErr(450, "")
	}
	if in.depth >= maxCallDepth {
		return nil, newErr(28, "")
	}
	fr := &frame{
		vars:     make(map[string]*Value, len(p.params)+4),
		proc:     p,
		me:       me,
		explicit: p.explicit,
		file:     p.file,
	}
	for i := range p.params {
		prm := &p.params[i]
		if prm.byVal {
			c := new(Value)
			*c = storeValue(*args[i])
			fr.vars[prm.lname] = c
			continue
		}
		if a, ok := (*args[i]).(*Array); ok && a.temp {
			a.temp = false
		}
		fr.vars[prm.lname] = args[i]
	}
	var ret *Value
	if p.returnsValue() {
		ret = new(Value)
		if _, clash := fr.vars[p.lname]; !clash {
			fr.vars[p.lname] = ret
		} else {
			ret = fr.vars[p.lname]
		}
	}
	in.frames = append(in.frames, fr)
	in.depth++
	err := in.declare(fr, p.localDecls())
	if err == nil {
		_, err = in.execBlock(fr, p.body)
	}
	in.depth--
	in.frames = in.frames[:len(in.frames)-1]
	if in.liveTerm > 0 {
		for _, c := range fr.vars {
			if inst, ok := (*c).(*classInstance); ok && inst.class.termProc != nil && !inst.terminated {
				in.gcPending = true
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if ret == nil {
		return nil, nil
	}
	rv := *ret
	if a, ok := rv.(*Array); ok {
		a.temp = true // the frame is gone: the caller may adopt the array
	}
	return rv, nil
}

// evalArgs evaluates arguments to plain values (for built-ins and host
// objects). Omitted arguments are passed as Empty.
func (in *Interpreter) evalArgs(f *frame, args []Expr) ([]Value, error) {
	out := make([]Value, len(args))
	for i, a := range args {
		if a == nil {
			continue
		}
		v, err := in.evalExpr(f, a)
		if err != nil {
			return nil, err
		}
		out[i] = v
		in.track(v)
	}
	return out, nil
}

// argCells evaluates arguments for a script procedure. Arguments that name
// a variable or an array element yield that storage cell so that ByRef
// parameters can modify it; anything else is passed in a fresh cell.
func (in *Interpreter) argCells(f *frame, args []Expr) ([]*Value, error) {
	if len(args) == 0 {
		return nil, nil
	}
	cells := make([]*Value, len(args))
	for i, a := range args {
		switch x := a.(type) {
		case nil:
			cells[i] = new(Value)
			continue
		case *Ident:
			c := in.lookupVarCell(f, x.lname)
			if c == nil && !f.explicit && in.resolve(f, x.lname).kind == nkNone {
				c = new(Value) // implicitly declared by being passed
				f.vars[x.lname] = c
			}
			if c != nil {
				cells[i] = c
				in.track(*c)
				continue
			}
		case *CallExpr:
			if id, ok := x.target.(*Ident); ok {
				if c := in.lookupVarCell(f, id.lname); c != nil {
					if arr, ok := (*c).(*Array); ok {
						idx, err := in.evalIndexes(f, x.args)
						if err != nil {
							return nil, locate(err, x.pos)
						}
						off, ok := arr.offset(idx)
						if !ok {
							return nil, locate(newErr(9, id.name), x.pos)
						}
						cells[i] = &arr.data[off]
						in.track(arr.data[off])
						continue
					}
				}
			}
		}
		v, err := in.evalExpr(f, a)
		if err != nil {
			return nil, err
		}
		if arr, ok := v.(*Array); ok && !arr.temp {
			// An array produced by an expression is a copy owned by the
			// callee.
			arr = arr.clone()
			arr.temp = true
			v = arr
		}
		c := new(Value)
		*c = v
		cells[i] = c
		in.track(v)
	}
	return cells, nil
}

// evalIndexes evaluates array subscripts.
func (in *Interpreter) evalIndexes(f *frame, args []Expr) ([]int, error) {
	idx := make([]int, len(args))
	for i, a := range args {
		if a == nil {
			return nil, newErr(9, "")
		}
		v, err := in.evalExpr(f, a)
		if err != nil {
			return nil, err
		}
		n, err := toInt(v)
		if err != nil {
			return nil, err
		}
		if n < 0 || n > maxArrayElems {
			return nil, newErr(9, "")
		}
		idx[i] = int(n)
	}
	return idx, nil
}

func toIndexes(args []Value) ([]int, error) {
	idx := make([]int, len(args))
	for i, a := range args {
		n, err := toInt(a)
		if err != nil {
			return nil, err
		}
		if n < 0 || n > maxArrayElems {
			return nil, newErr(9, "")
		}
		idx[i] = int(n)
	}
	return idx, nil
}

// indexInto applies an argument list to a value: an array subscript or a
// call of an object's default member.
func (in *Interpreter) indexInto(f *frame, v Value, args []Expr, name string) (Value, error) {
	mark := len(in.temps)
	in.track(v)
	defer func() { in.temps = in.temps[:mark] }()
	switch x := v.(type) {
	case *Array:
		idx, err := in.evalIndexes(f, args)
		if err != nil {
			return nil, err
		}
		off, ok := x.offset(idx)
		if !ok {
			return nil, newErr(9, name)
		}
		return x.data[off], nil
	case *classInstance:
		cells, err := in.argCells(f, args)
		if err != nil {
			return nil, err
		}
		r, err := in.instInvoke(x, "", cells, in.isInternal(f, x))
		return r, withDetail(err, name)
	}
	vals, err := in.evalArgs(f, args)
	if err != nil {
		return nil, err
	}
	r, err := in.indexValues(v, vals)
	return r, withDetail(err, name)
}

// indexValues is indexInto for already evaluated arguments.
func (in *Interpreter) indexValues(v Value, args []Value) (Value, error) {
	switch x := v.(type) {
	case *Array:
		idx, err := toIndexes(args)
		if err != nil {
			return nil, err
		}
		off, ok := x.offset(idx)
		if !ok {
			return nil, newErr(9, "")
		}
		return x.data[off], nil
	case *classInstance:
		return in.instInvoke(x, "", valueCells(args), false)
	case Object:
		if isNothing(v) {
			return nil, newErr(91, "")
		}
		in.noteEscapeTo(x, args)
		r, err := x.Get("", args)
		if err != nil {
			return nil, in.hostErr(err)
		}
		return normalize(r), nil
	}
	if len(args) == 0 {
		return v, nil
	}
	return nil, newErr(13, "")
}

// memberObject evaluates the object part of a member access; a nil object
// expression refers to the innermost With block.
func (in *Interpreter) memberObject(f *frame, m *MemberExpr) (Value, error) {
	if m.obj == nil {
		if len(f.withs) == 0 {
			return nil, newErr(505, "")
		}
		return f.withs[len(f.withs)-1], nil
	}
	return in.evalExpr(f, m.obj)
}

func (in *Interpreter) isInternal(f *frame, inst *classInstance) bool {
	return f.me != nil && f.me.class == inst.class
}

// evalMember evaluates `obj.name` / `obj.name(args)`.
func (in *Interpreter) evalMember(f *frame, m *MemberExpr, args []Expr) (Value, error) {
	obj, err := in.memberObject(f, m)
	if err != nil {
		return nil, err
	}
	switch o := obj.(type) {
	case *classInstance:
		mark := len(in.temps)
		in.track(obj)
		cells, err := in.argCells(f, args)
		if err != nil {
			return nil, err
		}
		v, err := in.instInvoke(o, m.lname, cells, in.isInternal(f, o))
		in.temps = in.temps[:mark]
		return v, withDetail(err, m.name)
	case Object:
		if isNothing(obj) {
			return nil, newErr(91, exprName(m.obj))
		}
		vals, err := in.evalArgs(f, args)
		if err != nil {
			return nil, err
		}
		in.noteEscapeTo(o, vals)
		r, err := o.Get(m.lname, vals)
		if err != nil {
			return nil, withDetail(in.hostErr(err), m.name)
		}
		return normalize(r), nil
	}
	return nil, newErr(424, exprName(m.obj))
}

// ---- assignment ----

// assign implements `target = v` and `Set target = v`.
func (in *Interpreter) assign(f *frame, target Expr, v Value, isSet bool) error {
	if isSet {
		if !isObjectRef(v) {
			return newErr(424, exprName(target))
		}
	} else if _, ok := asObject(v); ok {
		// Assigning an object without Set stores its default value. Objects
		// without a default member are stored by reference (VBScript would
		// raise error 438 here).
		if dv, err := defaultValue(v); err == nil {
			v = dv
		}
	}
	mark := len(in.temps)
	in.track(v)
	err := in.assignTo(f, target, v, isSet)
	in.temps = in.temps[:mark]
	return err
}

func (in *Interpreter) assignTo(f *frame, target Expr, v Value, isSet bool) error {
	switch t := target.(type) {
	case *Ident:
		return in.assignIdent(f, t, v, isSet)
	case *MemberExpr:
		obj, err := in.memberObject(f, t)
		if err != nil {
			return err
		}
		return in.assignMember(f, obj, t, nil, v, isSet)
	case *CallExpr:
		switch tt := t.target.(type) {
		case *Ident:
			ln := tt.lname
			if c, ok := f.vars[ln]; ok {
				return in.assignIndexed(f, *c, t.args, v, isSet, tt.name)
			}
			if f.me != nil {
				cl := f.me.class
				if cl.letters[ln] != nil || cl.setters[ln] != nil {
					vals, err := in.evalArgs(f, t.args)
					if err != nil {
						return err
					}
					return withDetail(in.instAssign(f.me, ln, vals, v, isSet, true), tt.name)
				}
			}
			if c := in.lookupVarCell(f, ln); c != nil {
				return in.assignIndexed(f, *c, t.args, v, isSet, tt.name)
			}
			if f.explicit && in.resolve(f, ln).kind == nkNone {
				return newErr(500, tt.name)
			}
			return newErr(13, tt.name)
		case *MemberExpr:
			obj, err := in.memberObject(f, tt)
			if err != nil {
				return err
			}
			vals, err := in.evalArgs(f, t.args)
			if err != nil {
				return err
			}
			return in.assignMember(f, obj, tt, vals, v, isSet)
		}
		cur, err := in.evalExpr(f, t.target)
		if err != nil {
			return err
		}
		return in.assignIndexed(f, cur, t.args, v, isSet, "")
	}
	return newErr(501, "")
}

func (in *Interpreter) assignIdent(f *frame, id *Ident, v Value, isSet bool) error {
	ln := id.lname
	if c, ok := f.vars[ln]; ok {
		in.setCell(c, v)
		return nil
	}
	if _, ok := f.consts[ln]; ok {
		return newErr(501, id.name)
	}
	if f.me != nil {
		if c, ok := f.me.fields[ln]; ok {
			in.setCell(c, v)
			return nil
		}
		cl := f.me.class
		if _, ok := cl.consts[ln]; ok {
			return newErr(501, id.name)
		}
		if cl.letters[ln] != nil || cl.setters[ln] != nil {
			return withDetail(in.instAssign(f.me, ln, nil, v, isSet, true), id.name)
		}
		if cl.methods[ln] != nil || cl.getters[ln] != nil {
			return newErr(501, id.name)
		}
	}
	if !f.global {
		if c, ok := in.globals[ln]; ok {
			in.setCell(c, v)
			return nil
		}
		if _, ok := in.gconsts[ln]; ok {
			return newErr(501, id.name)
		}
	}
	if in.procs[ln] != nil {
		return newErr(501, id.name)
	}
	if f.explicit {
		return newErr(500, id.name)
	}
	c := new(Value)
	f.vars[ln] = c
	in.setCell(c, v)
	return nil
}

// assignIndexed implements `container(args) = v`: an array element or the
// default property of an object.
func (in *Interpreter) assignIndexed(f *frame, container Value, args []Expr, v Value, isSet bool, name string) error {
	switch x := container.(type) {
	case *Array:
		idx, err := in.evalIndexes(f, args)
		if err != nil {
			return err
		}
		off, ok := x.offset(idx)
		if !ok {
			return newErr(9, name)
		}
		in.setCell(&x.data[off], v)
		return nil
	case *classInstance:
		vals, err := in.evalArgs(f, args)
		if err != nil {
			return err
		}
		return withDetail(in.instAssign(x, "", vals, v, isSet, in.isInternal(f, x)), name)
	case Object:
		if isNothing(container) {
			return newErr(91, name)
		}
		vals, err := in.evalArgs(f, args)
		if err != nil {
			return err
		}
		in.noteEscapeTo(x, vals)
		in.noteEscapeTo(x, []Value{v})
		return withDetail(in.hostErr(x.Set("", vals, v)), name)
	}
	return newErr(13, name)
}

// assignMember implements `obj.name = v` and `obj.name(args) = v`.
func (in *Interpreter) assignMember(f *frame, obj Value, m *MemberExpr, args []Value, v Value, isSet bool) error {
	switch o := obj.(type) {
	case *classInstance:
		return withDetail(in.instAssign(o, m.lname, args, v, isSet, in.isInternal(f, o)), m.name)
	case Object:
		if isNothing(obj) {
			return newErr(91, exprName(m.obj))
		}
		if args == nil {
			args = []Value{}
		}
		in.noteEscapeTo(o, args)
		in.noteEscapeTo(o, []Value{v})
		return withDetail(in.hostErr(o.Set(m.lname, args, v)), m.name)
	}
	return newErr(424, exprName(m.obj))
}
