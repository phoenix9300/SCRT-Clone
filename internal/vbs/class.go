package vbs

import "strings"

// classInstance is an instance of a script-defined Class. It implements
// Object so that hosts can call into script objects they were handed.
type classInstance struct {
	class      *classDef
	fields     map[string]*Value
	in         *Interpreter
	terminated bool // Class_Terminate has run
	escaped    bool // handed to a host object; kept alive until Close
}

// TypeName reports the class name.
func (o *classInstance) TypeName() string { return o.class.name }

// Get implements Object: it reads a public field or property, or calls a
// public method. An empty name designates the default member.
func (o *classInstance) Get(name string, args []Value) (Value, error) {
	norm := make([]Value, len(args))
	for i, a := range args {
		norm[i] = normalize(a)
	}
	v, err := o.in.instInvoke(o, strings.ToLower(name), valueCells(norm), false)
	if a, ok := v.(*Array); ok {
		a.temp = false
	}
	return v, withDetail(err, name)
}

// Set implements Object: it assigns a public field or calls Property Let
// (or Property Set when v is an object and no Property Let exists).
func (o *classInstance) Set(name string, args []Value, v Value) error {
	norm := make([]Value, len(args))
	for i, a := range args {
		norm[i] = normalize(a)
	}
	v = normalize(v)
	c := o.class
	ln := strings.ToLower(name)
	isSet := isObjectRef(v) && c.setters[ln] != nil
	return withDetail(o.in.instAssign(o, ln, norm, v, isSet, false), name)
}

// newInstance implements `New ClassName`.
func (in *Interpreter) newInstance(cd *classDef) (Value, error) {
	inst := &classInstance{class: cd, fields: make(map[string]*Value, len(cd.fields)), in: in}
	for i := range cd.fields {
		if err := in.declareVar(in.gframe, inst.fields, &cd.fields[i].dimDecl); err != nil {
			return nil, err
		}
	}
	if cd.termProc != nil {
		in.termList = append(in.termList, inst)
		in.liveTerm++
	}
	if cd.initProc != nil {
		mark := len(in.temps)
		in.track(inst)
		_, err := in.callProc(cd.initProc, inst, nil)
		in.temps = in.temps[:mark]
		if err != nil {
			return nil, err
		}
	}
	return inst, nil
}

// instInvoke reads a member of a class instance or calls one of its methods.
// internal is true when the caller is code of the same class, which may use
// private members. An empty name designates the default member.
func (in *Interpreter) instInvoke(inst *classInstance, ln string, args []*Value, internal bool) (Value, error) {
	c := inst.class
	if ln == "" {
		if c.defaultMem == "" {
			return nil, newErr(438, "")
		}
		ln = c.defaultMem
	}
	if p := c.methods[ln]; p != nil {
		if !p.public && !internal {
			return nil, newErr(438, "")
		}
		return in.callProcFlex(p, inst, args)
	}
	if p := c.getters[ln]; p != nil {
		if !p.public && !internal {
			return nil, newErr(438, "")
		}
		return in.callProcFlex(p, inst, args)
	}
	if cell, ok := inst.fields[ln]; ok {
		if !c.fieldPub[ln] && !internal {
			return nil, newErr(438, "")
		}
		if len(args) == 0 {
			return *cell, nil
		}
		vals := make([]Value, len(args))
		for i, a := range args {
			vals[i] = *a
		}
		return in.indexValues(*cell, vals)
	}
	if v, ok := c.consts[ln]; ok && internal {
		return v, nil
	}
	if c.letters[ln] != nil || c.setters[ln] != nil {
		return nil, newErr(450, "")
	}
	return nil, newErr(438, "")
}

// instAssign implements assignment to a member of a class instance:
// Property Let / Property Set, a field, or an element of an array field.
func (in *Interpreter) instAssign(inst *classInstance, ln string, args []Value, v Value, isSet, internal bool) error {
	c := inst.class
	if ln == "" {
		if c.defaultMem == "" {
			return newErr(438, "")
		}
		ln = c.defaultMem
	}
	let, set := c.letters[ln], c.setters[ln]
	var p *procDef
	switch {
	case isSet && set != nil:
		p = set
	case !isSet && let != nil:
		p = let
	case isSet && let != nil:
		p = let
	case !isSet && set != nil && isObjectRef(v):
		p = set
	}
	if p != nil {
		if !p.public && !internal {
			return newErr(438, "")
		}
		cells := valueCells(append(append(make([]Value, 0, len(args)+1), args...), v))
		_, err := in.callProc(p, inst, cells)
		return err
	}
	if cell, ok := inst.fields[ln]; ok {
		if !c.fieldPub[ln] && !internal {
			return newErr(438, "")
		}
		if len(args) == 0 {
			in.setCell(cell, v)
			return nil
		}
		switch x := (*cell).(type) {
		case *Array:
			idx, err := toIndexes(args)
			if err != nil {
				return err
			}
			off, ok := x.offset(idx)
			if !ok {
				return newErr(9, "")
			}
			in.setCell(&x.data[off], v)
			return nil
		case *classInstance:
			return in.instAssign(x, "", args, v, isSet, false)
		case Object:
			if isNothing(*cell) {
				return newErr(91, "")
			}
			in.noteEscapeTo(x, args)
			in.noteEscapeTo(x, []Value{v})
			return in.hostErr(x.Set("", args, v))
		}
		return newErr(13, "")
	}
	if c.getters[ln] != nil || c.methods[ln] != nil || set != nil {
		return newErr(450, "")
	}
	return newErr(438, "")
}

// procRef is the object returned by GetRef: calling its default member calls
// the procedure.
type procRef struct {
	in   *Interpreter
	proc *procDef
}

func (r *procRef) TypeName() string { return "Object" }

func (r *procRef) Get(name string, args []Value) (Value, error) {
	if name != "" {
		return nil, newErr(438, "")
	}
	return r.in.callProc(r.proc, nil, valueCells(args))
}

func (r *procRef) Set(string, []Value, Value) error { return newErr(438, "") }

// ---- Class_Terminate support ----
//
// VBScript runs Class_Terminate when the last reference to an instance goes
// away. Go has no reference counts, so the interpreter approximates this with
// a small reachability scan that only runs when instances of classes that
// define Class_Terminate exist and a reference to one of them was just
// dropped (a variable was overwritten or a procedure returned). Instances
// that are no longer reachable from script variables are terminated at the
// next statement boundary; whatever is still alive is terminated by Close.

// gcTracer is implemented by built-in containers so that the scan can see
// the values they hold.
type gcTracer interface {
	gcRefs() []Value
}

// track registers a temporary value as a root for the duration of the
// current statement.
func (in *Interpreter) track(v Value) {
	if in.liveTerm > 0 {
		in.temps = append(in.temps, v)
	}
}

// noteEscapeTo marks instances passed to an opaque host object: the
// interpreter cannot see whether the host keeps them, so they stay alive
// until Close.
func (in *Interpreter) noteEscapeTo(o Object, vals []Value) {
	if in.liveTerm == 0 {
		return
	}
	if _, ok := o.(gcTracer); ok {
		return
	}
	in.noteEscape(vals)
}

func (in *Interpreter) noteEscape(vals []Value) {
	if in.liveTerm == 0 {
		return
	}
	for _, v := range vals {
		switch x := v.(type) {
		case *classInstance:
			x.escaped = true
		case *Array:
			in.noteEscape(x.data)
		}
	}
}

// collect terminates instances that are no longer reachable.
func (in *Interpreter) collect() {
	in.gcPending = false
	if in.liveTerm == 0 || in.collecting {
		return
	}
	in.collecting = true
	defer func() { in.collecting = false }()

	seen := map[any]struct{}{}
	var mark func(v Value)
	mark = func(v Value) {
		switch x := v.(type) {
		case *classInstance:
			if _, ok := seen[x]; ok {
				return
			}
			seen[x] = struct{}{}
			for _, c := range x.fields {
				mark(*c)
			}
		case *Array:
			for _, e := range x.data {
				if e != nil {
					mark(e)
				}
			}
		case gcTracer:
			if _, ok := seen[x]; ok {
				return
			}
			seen[x] = struct{}{}
			for _, e := range x.gcRefs() {
				mark(e)
			}
		}
	}
	markFrame := func(fr *frame) {
		for _, c := range fr.vars {
			mark(*c)
		}
		if fr.me != nil {
			mark(fr.me)
		}
		for _, w := range fr.withs {
			mark(w)
		}
	}
	markFrame(in.gframe)
	for _, fr := range in.frames {
		if fr != in.gframe {
			markFrame(fr)
		}
	}
	for _, t := range in.temps {
		mark(t)
	}

	old := in.termList
	in.termList = nil
	var dead []*classInstance
	for _, inst := range old {
		if _, ok := seen[inst]; ok || inst.escaped {
			in.termList = append(in.termList, inst)
		} else {
			dead = append(dead, inst)
		}
	}
	for _, inst := range dead {
		in.terminate(inst)
	}
}

// terminate runs Class_Terminate of an instance once.
func (in *Interpreter) terminate(inst *classInstance) {
	if inst.terminated {
		return
	}
	inst.terminated = true
	in.liveTerm--
	if p := inst.class.termProc; p != nil {
		_, _ = in.callProc(p, inst, nil)
	}
}
