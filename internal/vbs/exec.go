package vbs

import "time"

// execBlock runs a statement list. It implements On Error Resume Next: when
// the frame has it enabled, a trappable error is recorded in Err and
// execution continues with the next statement.
func (in *Interpreter) execBlock(f *frame, stmts []Stmt) (ctrl, error) {
	for _, s := range stmts {
		c, err := in.execStmt(f, s)
		if err != nil {
			if in.absorb(f, err) {
				continue
			}
			return ctrlNone, err
		}
		if c != ctrlNone {
			return c, nil
		}
	}
	return ctrlNone, nil
}

// absorb records err in the Err object and reports true when the frame runs
// under On Error Resume Next. Only script errors can be absorbed; context
// cancellation and WScript.Quit always propagate.
func (in *Interpreter) absorb(f *frame, err error) bool {
	if !f.resumeNext {
		return false
	}
	e, ok := err.(*Error)
	if !ok {
		return false
	}
	in.errObj.capture(e)
	return true
}

func (in *Interpreter) execStmt(f *frame, s Stmt) (ctrl, error) {
	if in.done != nil {
		select {
		case <-in.done:
			return ctrlNone, in.ctx.Err()
		default:
		}
	}
	mark := len(in.temps)
	c, err := in.exec1(f, s)
	if err != nil {
		err = in.located(err, f, s.getPos())
	}
	if len(in.temps) > mark {
		in.temps = in.temps[:mark]
	}
	if in.gcPending {
		in.collect()
	}
	return c, err
}

func (in *Interpreter) exec1(f *frame, s Stmt) (ctrl, error) {
	switch s := s.(type) {
	case *AssignStmt:
		v, err := in.evalExpr(f, s.val)
		if err != nil {
			return ctrlNone, err
		}
		return ctrlNone, in.assign(f, s.target, v, s.isSet)
	case *ExprStmt:
		return ctrlNone, in.execCall(f, s)
	case *IfStmt:
		return in.execIf(f, s)
	case *ForStmt:
		return in.execFor(f, s)
	case *ForEachStmt:
		return in.execForEach(f, s)
	case *DoStmt:
		return in.execDo(f, s)
	case *SelectStmt:
		return in.execSelect(f, s)
	case *WithStmt:
		obj, err := in.evalExpr(f, s.obj)
		if err != nil {
			return ctrlNone, err
		}
		f.withs = append(f.withs, obj)
		c, err := in.execBlock(f, s.body)
		f.withs = f.withs[:len(f.withs)-1]
		return c, err
	case *ExitStmt:
		return s.kind, nil
	case *OnErrorStmt:
		f.resumeNext = s.resume
		in.errObj.clear()
		return ctrlNone, nil
	case *ReDimStmt:
		return ctrlNone, in.execReDim(f, s)
	case *EraseStmt:
		return ctrlNone, in.execErase(f, s)
	case *DimStmt, *ConstStmt, *ProcStmt, *ClassStmt, *OptionStmt:
		// Declarations take effect when their scope is entered.
		return ctrlNone, nil
	}
	return ctrlNone, newErrMsg(51, "Internal error: unknown statement")
}

// execCall runs a call statement.
func (in *Interpreter) execCall(f *frame, s *ExprStmt) error {
	v, err := in.evalExpr(f, s.x)
	if err != nil {
		return err
	}
	// A bare reference to a GetRef() result calls the procedure.
	if pr, ok := v.(*procRef); ok {
		switch s.x.(type) {
		case *Ident, *MemberExpr:
			_, err = pr.Get("", nil)
		}
	}
	return err
}

// evalCond evaluates a branch or loop condition. When the evaluation fails
// under On Error Resume Next the error is recorded and failed is true.
func (in *Interpreter) evalCond(f *frame, e Expr) (ok, failed bool, err error) {
	v, err := in.evalExpr(f, e)
	if err == nil {
		ok, err = condBool(v)
	}
	if err != nil {
		err = in.located(err, f, e.getPos())
		if in.absorb(f, err) {
			return false, true, nil
		}
		return false, false, err
	}
	return ok, false, nil
}

func (in *Interpreter) execIf(f *frame, s *IfStmt) (ctrl, error) {
	for i := range s.branches {
		br := &s.branches[i]
		ok, failed, err := in.evalCond(f, br.cond)
		if err != nil {
			return ctrlNone, err
		}
		// VBScript resumes with the statement following the failed
		// condition, which is the first statement of the block.
		if ok || failed {
			return in.execBlock(f, br.body)
		}
	}
	if s.hasElse {
		return in.execBlock(f, s.els)
	}
	return ctrlNone, nil
}

// loopNumber prepares a For loop bound.
func loopNumber(v Value) (Value, error) {
	v, err := defaultValue(v)
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case int64, float64, time.Time:
		return v, nil
	case nil:
		return int64(0), nil
	case bool:
		return boolNum(x), nil
	}
	f, err := toFloat(v)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (in *Interpreter) execFor(f *frame, s *ForStmt) (ctrl, error) {
	from, err := in.evalExpr(f, s.from)
	if err != nil {
		return ctrlNone, err
	}
	to, err := in.evalExpr(f, s.to)
	if err != nil {
		return ctrlNone, err
	}
	var step Value = int64(1)
	if s.step != nil {
		if step, err = in.evalExpr(f, s.step); err != nil {
			return ctrlNone, err
		}
	}
	if from, err = loopNumber(from); err != nil {
		return ctrlNone, err
	}
	if to, err = loopNumber(to); err != nil {
		return ctrlNone, err
	}
	if step, err = loopNumber(step); err != nil {
		return ctrlNone, err
	}
	toF, _ := toFloat(to)
	stepF, _ := toFloat(step)
	toI, toIsInt := to.(int64)
	stepI, stepIsInt := step.(int64)
	cell, err := in.varCell(f, s.name, s.lname, false)
	if err != nil {
		return ctrlNone, err
	}
	*cell = from
	for {
		if err := in.checkCtx(); err != nil {
			return ctrlNone, err
		}
		if cur, ok := (*cell).(int64); ok && toIsInt && stepIsInt {
			if (stepI >= 0 && cur > toI) || (stepI < 0 && cur < toI) {
				break
			}
		} else {
			cur, err := toFloat(*cell)
			if err != nil {
				return ctrlNone, err
			}
			if (stepF >= 0 && cur > toF) || (stepF < 0 && cur < toF) {
				break
			}
		}
		c, err := in.execBlock(f, s.body)
		if err != nil {
			return ctrlNone, err
		}
		if c == ctrlExitFor {
			break
		}
		if c != ctrlNone {
			return c, nil
		}
		nv, err := arithOp(opAdd, *cell, step)
		if err != nil {
			return ctrlNone, err
		}
		*cell = nv
	}
	return ctrlNone, nil
}

func (in *Interpreter) execForEach(f *frame, s *ForEachStmt) (ctrl, error) {
	coll, err := in.evalExpr(f, s.coll)
	if err != nil {
		return ctrlNone, err
	}
	var items []Value
	switch x := coll.(type) {
	case *Array:
		items = append(items, x.data...)
	case Enumerable:
		if isNothing(coll) {
			return ctrlNone, newErr(91, "")
		}
		items = x.Enumerate()
	default:
		return ctrlNone, newErr(451, "")
	}
	cell, err := in.varCell(f, s.name, s.lname, false)
	if err != nil {
		return ctrlNone, err
	}
	mark := len(in.temps)
	in.track(coll)
	defer func() { in.temps = in.temps[:mark] }()
	for _, it := range items {
		if err := in.checkCtx(); err != nil {
			return ctrlNone, err
		}
		in.setCell(cell, normalize(it))
		c, err := in.execBlock(f, s.body)
		if err != nil {
			return ctrlNone, err
		}
		if c == ctrlExitFor {
			break
		}
		if c != ctrlNone {
			return c, nil
		}
	}
	return ctrlNone, nil
}

func (in *Interpreter) execDo(f *frame, s *DoStmt) (ctrl, error) {
	for {
		if err := in.checkCtx(); err != nil {
			return ctrlNone, err
		}
		if s.pre != nil {
			ok, failed, err := in.evalCond(f, s.pre)
			if err != nil {
				return ctrlNone, err
			}
			// A failed condition under On Error Resume Next falls into
			// the loop body, as it does in VBScript.
			if !failed && ok == s.preUntil {
				break
			}
		}
		c, err := in.execBlock(f, s.body)
		if err != nil {
			return ctrlNone, err
		}
		if c == ctrlExitDo {
			break
		}
		if c != ctrlNone {
			return c, nil
		}
		if s.post != nil {
			ok, failed, err := in.evalCond(f, s.post)
			if err != nil {
				return ctrlNone, err
			}
			if failed || ok == s.postUntil {
				break
			}
		}
	}
	return ctrlNone, nil
}

func (in *Interpreter) execSelect(f *frame, s *SelectStmt) (ctrl, error) {
	sel, err := in.evalExpr(f, s.expr)
	if err != nil {
		return ctrlNone, err
	}
	for i := range s.cases {
		cc := &s.cases[i]
		for _, ve := range cc.vals {
			cv, err := in.evalExpr(f, ve)
			if err != nil {
				return ctrlNone, err
			}
			c, null, err := compareValues(sel, cv, false, isHardLit(ve))
			if err != nil {
				return ctrlNone, in.located(err, f, ve.getPos())
			}
			if !null && c == 0 {
				return in.execBlock(f, cc.body)
			}
		}
	}
	if s.hasElse {
		return in.execBlock(f, s.els)
	}
	return ctrlNone, nil
}

// lvalueCell returns the storage cell of a variable or class field named by
// target, creating a variable when declare is set (ReDim declares).
func (in *Interpreter) lvalueCell(f *frame, target Expr, declare bool) (*Value, error) {
	switch t := target.(type) {
	case *Ident:
		return in.varCell(f, t.name, t.lname, declare)
	case *MemberExpr:
		obj, err := in.memberObject(f, t)
		if err != nil {
			return nil, err
		}
		if inst, ok := obj.(*classInstance); ok {
			if c, ok := inst.fields[t.lname]; ok {
				if !inst.class.fieldPub[t.lname] && !in.isInternal(f, inst) {
					return nil, newErr(438, t.name)
				}
				return c, nil
			}
		}
		return nil, newErr(13, t.name)
	}
	return nil, newErr(13, "")
}

func (in *Interpreter) execReDim(f *frame, s *ReDimStmt) error {
	for _, it := range s.items {
		dims, err := in.evalDims(f, it.dims)
		if err != nil {
			return err
		}
		cell, err := in.lvalueCell(f, it.target, true)
		if err != nil {
			return err
		}
		old, _ := (*cell).(*Array)
		if old != nil && old.fixed {
			return newErr(10, "")
		}
		var na *Array
		if s.preserve && old != nil && len(old.dims) > 0 {
			if len(old.dims) != len(dims) {
				return newErr(9, "")
			}
			for i := 0; i < len(dims)-1; i++ {
				if old.dims[i] != dims[i] {
					return newErr(9, "")
				}
			}
			na, err = old.resizeLast(dims[len(dims)-1])
		} else {
			na, err = newArrayDims(dims)
		}
		if err != nil {
			return err
		}
		*cell = na
	}
	return nil
}

func (in *Interpreter) execErase(f *frame, s *EraseStmt) error {
	cell, err := in.lvalueCell(f, s.target, false)
	if err != nil {
		return err
	}
	arr, ok := (*cell).(*Array)
	if !ok {
		return newErr(13, "")
	}
	if arr.fixed {
		for i := range arr.data {
			arr.data[i] = nil
		}
		return nil
	}
	*cell = &Array{}
	return nil
}
