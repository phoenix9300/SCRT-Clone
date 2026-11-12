package vbs

import "testing"

func TestProcedureArguments(t *testing.T) {
	runCases(t, []tcase{
		{"byval literal and expression", `
Sub S(ByVal a, ByRef b) : a = a + 1 : b = b + 1 : End Sub
x = 1 : y = 1
S x, y
WScript.Echo x, y
S x + 0, y + 0
WScript.Echo x, y`, "1 2\n1 2"},
		{"parenthesised arg by value", `
Sub Inc(n) : n = n + 1 : End Sub
x = 1
Inc (x)
WScript.Echo x
Inc x
WScript.Echo x
Call Inc(x)
WScript.Echo x`, "1\n2\n3"},
		{"parenthesised in function call", `
Function Inc(n) : n = n + 1 : Inc = n : End Function
x = 1
r = Inc((x))
WScript.Echo x, r
r = Inc(x)
WScript.Echo x, r`, "1 2\n2 2"},
		{"byref to property of object is by value", `
Sub Inc(n) : n = n + 1 : End Sub
Set d = CreateObject("Scripting.Dictionary")
d.Add "k", 1
Inc d("k")
WScript.Echo d("k")`, "1"},
		{"byval does not alias array", `
Sub F(ByVal a) : a(0) = 9 : End Sub
b = Array(1)
F b
WScript.Echo b(0)`, "1"},
		{"optional param via IsEmpty", `
Sub S(a, b) : WScript.Echo IsEmpty(b) : End Sub
S 1, Empty`, "True"},
		{"function returns array", `
Function F() : F = Array(1, 2, 3) : End Function
WScript.Echo UBound(F()), F()(1)`, "2 2"},
		{"function returns object", `
Function F() : Set F = CreateObject("Scripting.Dictionary") : End Function
Set d = F()
d.Add "a", 1
WScript.Echo d.Count`, "1"},
		{"function without return is empty", `
Function F() : End Function
WScript.Echo IsEmpty(F())`, "True"},
		{"function call no parens statement", `
Function F(a) : WScript.Echo "f" & a : F = 1 : End Function
F 1
Call F(2)`, "f1\nf2"},
		{"recursion fib", `
Function Fib(n) : If n < 2 Then Fib = n Else Fib = Fib(n - 1) + Fib(n - 2)
End Function
WScript.Echo Fib(15)`, "610"},
		{"mutual recursion", `
Function IsEven(n) : If n = 0 Then IsEven = True Else IsEven = IsOdd(n - 1)
End Function
Function IsOdd(n) : If n = 0 Then IsOdd = False Else IsOdd = IsEven(n - 1)
End Function
WScript.Echo IsEven(10), IsOdd(7)`, "True True"},
		{"exit function keeps value", `
Function F(n)
  F = "early"
  If n > 0 Then Exit Function
  F = "late"
End Function
WScript.Echo F(1), F(0)`, "early late"},
		{"exit sub", `
Sub S(n)
  If n Then Exit Sub
  WScript.Echo "ran"
End Sub
S True : S False`, "ran"},
		{"exit for in function", `
Function F()
  For i = 1 To 10
    If i = 3 Then F = i : Exit Function
  Next
End Function
WScript.Echo F()`, "3"},
		{"globals visible in procs", `
g = 5
Sub S : g = g + 1 : End Sub
S
WScript.Echo g`, "6"},
		{"private and public sub", `
Private Sub A : WScript.Echo "a" : End Sub
Public Sub B : A : End Sub
B`, "a"},
		{"public default function", `
Public Function F : F = 1 : End Function
WScript.Echo F`, "1"},
		{"call with object method", `
Set d = CreateObject("Scripting.Dictionary")
Call d.Add("a", 1)
d.Add "b", 2
WScript.Echo d.Count`, "2"},
		{"case insensitive names", `
Function MyFunc() : MYFUNC = 3 : End Function
WScript.Echo myfunc()`, "3"},
		{"getref", `
Function Add(a, b) : Add = a + b : End Function
Set f = GetRef("Add")
WScript.Echo f(2, 3), TypeName(f)`, "5 Object"},
		{"getref sub", `
Sub Hello(n) : WScript.Echo "hi " & n : End Sub
Dim f : Set f = GetRef("Hello")
f "x"
Call f("y")`, "hi x\nhi y"},
		{"getref passed as arg", `
Function Sq(x) : Sq = x * x : End Function
Function Apply(f, v) : Apply = f(v) : End Function
WScript.Echo Apply(GetRef("Sq"), 7)`, "49"},
		{"stack overflow", `
Sub R : R : End Sub
On Error Resume Next
R
WScript.Echo Err.Number`, "28"},
	})
	runErrCases(t, []errCase{
		{"wrong arg count", "Sub S(a)\nEnd Sub\nS 1, 2", 450, 3, false},
		{"getref unknown", "Set f = GetRef(\"nope\")", 5, 1, false},
		{"duplicate sub", "Sub A\nEnd Sub\nSub A\nEnd Sub", 1041, 3, true},
	})
}

func TestExecuteEval(t *testing.T) {
	runCases(t, []tcase{
		{"eval", `WScript.Echo Eval("1 + 2 * 3"), Eval("""a"" & ""b"""), Eval("1 = 1")`, "7 ab True"},
		{"eval uses variables", `x = 4 : WScript.Echo Eval("x * x")`, "16"},
		{"eval in procedure sees locals", `
Sub S : Dim loc : loc = 9 : WScript.Echo Eval("loc + 1") : End Sub
S`, "10"},
		{"execute defines variable", `
Execute "y = 5"
WScript.Echo y`, "5"},
		{"execute multi statement", `
Execute "a = 1 : b = 2" & vbCrLf & "c = a + b"
WScript.Echo c`, "3"},
		{"execute defines sub", `
Execute "Sub Hello : WScript.Echo ""hello"" : End Sub"
Hello`, "hello"},
		{"executeglobal defines function", `
ExecuteGlobal "Function Twice(n) : Twice = n * 2 : End Function"
WScript.Echo Twice(4)`, "8"},
		{"executeglobal from procedure", `
Sub S : ExecuteGlobal "gv = 42" : End Sub
S
WScript.Echo gv`, "42"},
		{"execute local scope", `
Sub S
  Dim a : a = 1
  Execute "a = a + 1"
  WScript.Echo a
End Sub
S`, "2"},
		{"execute syntax error trapped", `
On Error Resume Next
Execute "x = = 1"
WScript.Echo Err.Number <> 0`, "True"},
		{"eval runtime error trapped", `
On Error Resume Next
v = Eval("1 / 0")
WScript.Echo Err.Number`, "11"},
		{"eval is equal sign", `WScript.Echo Eval("3 = 3"), Eval("2 > 3")`, "True False"},
	})
}

func TestClasses(t *testing.T) {
	runCases(t, []tcase{
		{"fields methods and me", `
Class Counter
  Private n
  Private Sub Class_Initialize : n = 10 : End Sub
  Public Sub Inc : n = n + 1 : End Sub
  Public Function Value : Value = n : End Function
  Public Function Self : Set Self = Me : End Function
End Class
Set c = New Counter
c.Inc : c.Inc
WScript.Echo c.Value, c.Self.Value`, "12 12"},
		{"initialize and terminate order", `
Class T
  Public Name
  Sub Class_Initialize : WScript.Echo "init" : End Sub
  Sub Class_Terminate : WScript.Echo "term " & Name : End Sub
End Class
Set t1 = New T
t1.Name = "one"
WScript.Echo "middle"
Set t1 = Nothing
WScript.Echo "end"`, "init\nmiddle\nterm one\nend"},
		{"terminate on reassignment", `
Class T
  Sub Class_Terminate : WScript.Echo "term" : End Sub
End Class
Set a = New T
Set a = New T
WScript.Echo "after"`, "term\nafter"},
		{"terminate on close", `
Class T
  Sub Class_Terminate : WScript.Echo "term" : End Sub
End Class
Set a = New T
WScript.Echo "body"`, "body"},
		{"property get let set", `
Class P
  Private v, o
  Public Property Get Value : Value = v : End Property
  Public Property Let Value(x) : v = x * 2 : End Property
  Public Property Get Obj : Set Obj = o : End Property
  Public Property Set Obj(x) : Set o = x : End Property
End Class
Set p = New P
p.Value = 5
Set p.Obj = CreateObject("Scripting.Dictionary")
p.Obj.Add "k", 1
WScript.Echo p.Value, p.Obj.Count`, "10 1"},
		{"property with index", `
Class Bag
  Private d
  Sub Class_Initialize : Set d = CreateObject("Scripting.Dictionary") : End Sub
  Public Property Get Item(k) : Item = d(k) : End Property
  Public Property Let Item(k, v) : d(k) = v : End Property
End Class
Set b = New Bag
b.Item("a") = 5
WScript.Echo b.Item("a")`, "5"},
		{"default property", `
Class E
  Public Default Function Item(i) : Item = "item" & i : End Function
End Class
Set e = New E
WScript.Echo e(3), e.Item(4)`, "item3 item4"},
		{"default property as value", `
Class D
  Public Default Property Get V : V = 7 : End Property
End Class
Set d = New D
WScript.Echo d + 1, d & "x"`, "8 7x"},
		{"public field and private field", `
Class F
  Public a
  Private b
  Sub SetB : b = 2 : End Sub
  Function GetB : GetB = b : End Function
End Class
Set f = New F
f.a = 1 : f.SetB
WScript.Echo f.a, f.GetB`, "1 2"},
		{"class methods call each other unqualified", `
Class M
  Function A : A = B() + 1 : End Function
  Private Function B : B = 1 : End Function
End Class
Dim r : r = (New M).A
WScript.Echo r`, "2"},
		{"class instances independent", `
Class C : Public v : End Class
Set a = New C : Set b = New C
a.v = 1 : b.v = 2
WScript.Echo a.v, b.v`, "1 2"},
		{"class with array field", `
Class L
  Public items
  Sub Class_Initialize : ReDim items(-1 + 1) : End Sub
End Class
Set l = New L
WScript.Echo UBound(l.items)`, "0"},
		{"object is nothing check", `
Class C : End Class
Set c = New C
WScript.Echo c Is Nothing, TypeName(c)`, "False C"},
		{"static method returns object", `
Class Pt
  Public X, Y
  Public Function Add(o) : Set Add = New Pt : Add.X = X + o.X : Add.Y = Y + o.Y : End Function
End Class
Set a = New Pt : a.X = 1 : a.Y = 2
Set b = New Pt : b.X = 10 : b.Y = 20
Set c = a.Add(b)
WScript.Echo c.X, c.Y`, "11 22"},
		{"class in with block", `
Class C : Public v : End Class
Set c = New C
With c
  .v = 4
  WScript.Echo .v
End With`, "4"},
		{"object compare is", `
Class C : End Class
Set a = New C : Set b = a : Set c = New C
WScript.Echo a Is b, a Is c`, "True False"},
		{"private function inaccessible via me", `
Class C
  Private Function Hid : Hid = 1 : End Function
  Public Function Pub : Pub = Me.Pub2 : End Function
  Public Function Pub2 : Pub2 = 2 : End Function
End Class
Dim r : r = (New C).Pub
WScript.Echo r`, "2"},
	})
	runErrCases(t, []errCase{
		{"private field from outside", "Class C\nPrivate x\nEnd Class\nSet o = New C\no.x = 1", 438, 5, false},
		{"private method from outside", "Class C\nPrivate Sub S\nEnd Sub\nEnd Class\nSet o = New C\no.S", 438, 6, false},
		{"private property from outside", "Class C\nPrivate Property Get P\nP = 1\nEnd Property\nEnd Class\nSet o = New C\nx = o.P", 438, 7, false},
		{"unknown member", "Class C\nEnd Class\nSet o = New C\no.Foo", 438, 4, false},
		{"unknown class", "Set o = New Nope", 506, 1, false},
		{"no default member", "Class C\nEnd Class\nSet o = New C\nx = o(1)", 438, 4, false},
		{"duplicate class", "Class C\nEnd Class\nClass C\nEnd Class", 1041, 3, true},
		{"set on let only property", "Class C\nPublic Property Get P : P = 1 : End Property\nEnd Class\nSet o = New C\no.P = 2", 450, 5, false},
	})
}

func TestClassTerminateOnClose(t *testing.T) {
	in, _, buf := newHost()
	err := in.Run(t.Context(), `
Class T
  Sub Class_Terminate : WScript.Echo "bye" : End Sub
End Class
Set a = New T
WScript.Echo "body"`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	if got := lines(buf); got != "body" {
		t.Fatalf("before Close: %q", got)
	}
	in.Close()
	if got := lines(buf); got != "body\nbye" {
		t.Fatalf("after Close: %q", got)
	}
}
