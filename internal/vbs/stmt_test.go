package vbs

import "testing"

func TestDeclarationsAndAssignment(t *testing.T) {
	runCases(t, []tcase{
		{"dim and assign", `
Dim a, b
a = 1 : b = "x"
WScript.Echo a & b`, "1x"},
		{"implicit variables", `
x = 5
y = x * 2
WScript.Echo y, IsEmpty(neverSet)`, "10 True"},
		{"option explicit ok", `
Option Explicit
Dim total
total = 3
WScript.Echo total`, "3"},
		{"dim is hoisted", `
Option Explicit
x = 4
WScript.Echo x
Dim x`, "4"},
		{"const", `
Const X = 5, Y = "s"
Const NEG = -2
Private Const P = 1.5
WScript.Echo X & Y, NEG, P`, "5s -2 1.5"},
		{"const visible before its line", `
Show
Const GREETING = "hi"
Sub Show : WScript.Echo GREETING : End Sub`, "hi"},
		{"public private at module level", `
Public a
Private b, c(2)
a = 1 : b = 2 : c(2) = 3
WScript.Echo a + b + c(2)`, "6"},
		{"let keyword", `
Let x = 7
WScript.Echo x`, "7"},
		{"set and nothing", `
Dim d
Set d = CreateObject("Scripting.Dictionary")
WScript.Echo d Is Nothing
Set d = Nothing
WScript.Echo d Is Nothing, IsObject(d)`, "False\nTrue True"},
		{"bracketed identifier", `
Dim [my var]
[my var] = 3
WScript.Echo [my var] + 1`, "4"},
	})
}

func TestArrays(t *testing.T) {
	runCases(t, []tcase{
		{"fixed array", `
Dim a(2)
a(0) = 1 : a(2) = 3
WScript.Echo UBound(a), LBound(a), a(0), IsEmpty(a(1)), a(2)`, "2 0 1 True 3"},
		{"multi dimensional", `
Dim m(1, 2)
m(1, 2) = 5 : m(0, 0) = "z"
WScript.Echo UBound(m, 1), UBound(m, 2), m(1, 2), m(0, 0), IsEmpty(m(1, 1))`, "1 2 5 z True"},
		{"redim preserve", `
Dim a()
ReDim a(1)
a(0) = "x" : a(1) = "y"
ReDim Preserve a(3)
a(3) = "z"
WScript.Echo UBound(a), a(0), a(1), IsEmpty(a(2)), a(3)
ReDim Preserve a(0)
WScript.Echo UBound(a), a(0)
ReDim a(2)
WScript.Echo UBound(a), IsEmpty(a(0))`, "3 x y True z\n0 x\n2 True"},
		{"redim preserve multi", `
Dim g()
ReDim g(1, 1)
g(0, 0) = "a" : g(1, 0) = "b" : g(0, 1) = "c" : g(1, 1) = "d"
ReDim Preserve g(1, 3)
g(1, 3) = "e"
WScript.Echo g(0, 0) & g(1, 0) & g(0, 1) & g(1, 1) & g(1, 3), UBound(g, 2)`, "abcde 3"},
		{"redim declares", `
Option Explicit
ReDim list(2)
list(1) = "ok"
WScript.Echo list(1), UBound(list)`, "ok 2"},
		{"redim with expressions and several arrays", `
n = 2
ReDim a(n * 2), b(n)
WScript.Echo UBound(a), UBound(b)`, "4 2"},
		{"array function", `
a = Array(10, "two", 3.5)
WScript.Echo UBound(a), a(0), a(1), a(2), IsArray(a), TypeName(a)`, "2 10 two 3.5 True Variant()"},
		{"arrays are copied on assignment", `
a = Array(1, 2, 3)
b = a
b(0) = 99
WScript.Echo a(0), b(0)`, "1 99"},
		{"nested arrays", `
a = Array(Array(1, 2), Array(3, 4))
WScript.Echo a(1)(0), UBound(a), UBound(a(0))
a(1)(0) = 30
WScript.Echo a(1)(0)`, "3 1 1\n30"},
		{"erase", `
Dim f(2)
f(1) = 5
Erase f
WScript.Echo UBound(f), IsEmpty(f(1))
Dim d()
ReDim d(3)
Erase d
On Error Resume Next
x = UBound(d)
WScript.Echo Err.Number`, "2 True\n9"},
		{"growing a list", `
Dim items()
ReDim items(-1)
For i = 1 To 4
  ReDim Preserve items(UBound(items) + 1)
  items(UBound(items)) = i * i
Next
WScript.Echo Join(items, ",")`, "1,4,9,16"},
		{"array element by float index rounds", `
a = Array("a", "b", "c")
WScript.Echo a(1.4), a("2")`, "b c"},
	})
}

func TestIf(t *testing.T) {
	runCases(t, []tcase{
		{"block", `
x = 5
If x > 10 Then
  WScript.Echo "big"
ElseIf x > 3 Then
  WScript.Echo "medium"
Else
  WScript.Echo "small"
End If`, "medium"},
		{"block else", `
If False Then
  WScript.Echo 1
ElseIf False Then
  WScript.Echo 2
Else
  WScript.Echo 3
End If`, "3"},
		{"single line", `
x = 5
If x > 3 Then WScript.Echo "big"
If x < 3 Then WScript.Echo "small"
WScript.Echo "done"`, "big\ndone"},
		{"single line else", `
x = 5
If x > 3 Then WScript.Echo "big" Else WScript.Echo "small"
If x < 3 Then WScript.Echo "big" Else WScript.Echo "small"`, "big\nsmall"},
		{"single line with colons", `
x = 5
If x < 3 Then WScript.Echo "a" : WScript.Echo "b" Else WScript.Echo "c" : WScript.Echo "d"
If x > 3 Then WScript.Echo "e" : WScript.Echo "f" Else WScript.Echo "g"
WScript.Echo "h"`, "c\nd\ne\nf\nh"},
		{"single line nested", `
x = 5
If x = 5 Then If x > 9 Then WScript.Echo "in" Else WScript.Echo "inner-else"
If x = 1 Then WScript.Echo 1 ElseIf x = 5 Then WScript.Echo 5 Else WScript.Echo 0`, "inner-else\n5"},
		{"single line with end if", `
If True Then WScript.Echo "endif" End If
WScript.Echo "next"`, "endif\nnext"},
		{"single line assignment and exit", `
For i = 1 To 5
  If i = 3 Then r = "three" : Exit For
Next
WScript.Echo i, r`, "3 three"},
		{"elseif with statement on the same line", `
x = 2
If x = 1 Then
  WScript.Echo "one"
ElseIf x = 2 Then WScript.Echo "two"
Else WScript.Echo "other"
End If`, "two"},
		{"nested blocks", `
For i = 1 To 3
  If i Mod 2 = 0 Then
    If i > 1 Then
      WScript.Echo "even", i
    End If
  Else
    WScript.Echo "odd", i
  End If
Next`, "odd 1\neven 2\nodd 3"},
		{"null and empty conditions", `
If Null Then WScript.Echo "t" Else WScript.Echo "null is false"
If Empty Then WScript.Echo "t" Else WScript.Echo "empty is false"
If "True" Then WScript.Echo "string true"
If 2 Then WScript.Echo "nonzero"`, "null is false\nempty is false\nstring true\nnonzero"},
	})
}

func TestSelectCase(t *testing.T) {
	runCases(t, []tcase{
		{"numbers", `
For i = 1 To 5
  Select Case i
    Case 1
      WScript.Echo "one"
    Case 2, 3
      WScript.Echo "two or three"
    Case Else
      WScript.Echo "other"
  End Select
Next`, "one\ntwo or three\ntwo or three\nother\nother"},
		{"strings", `
s = "b"
Select Case s
  Case "a" : WScript.Echo "A"
  Case "b" : WScript.Echo "B"
  Case Else : WScript.Echo "?"
End Select`, "B"},
		{"no match and no else", `
Select Case 9
  Case 1
    WScript.Echo "no"
End Select
WScript.Echo "after"`, "after"},
		{"expressions and first match wins", `
x = 4
Select Case True
  Case x < 3
    WScript.Echo "small"
  Case x < 5
    WScript.Echo "medium"
  Case x < 10
    WScript.Echo "large"
End Select`, "medium"},
		{"string selector with numeric cases", `
s = "2"
Select Case s
  Case 1 : WScript.Echo "one"
  Case 2 : WScript.Echo "two"
End Select`, "two"},
		{"exit inside select", `
For i = 1 To 3
  Select Case i
    Case 2
      Exit For
  End Select
  WScript.Echo i
Next`, "1"},
	})
}

func TestLoops(t *testing.T) {
	runCases(t, []tcase{
		{"for", `
s = ""
For i = 1 To 5
  s = s & i
Next
WScript.Echo s, i`, "12345 6"},
		{"for step", `
s = ""
For i = 10 To 1 Step -3
  s = s & i & " "
Next
For j = 0 To 1 Step 0.25
  s = s & j & " "
Next i
WScript.Echo s`, "10 7 4 1 0 0.25 0.5 0.75 1 "},
		{"for zero iterations", `
For i = 5 To 1
  WScript.Echo "never"
Next
WScript.Echo i`, "5"},
		{"for bounds evaluated once", `
n = 3
For i = 1 To n
  n = 10
  c = c + 1
Next
WScript.Echo c`, "3"},
		{"for exit and nesting", `
For i = 1 To 3
  For j = 1 To 3
    If j = 2 Then Exit For
    WScript.Echo i & "-" & j
  Next
Next`, "1-1\n2-1\n3-1"},
		{"for each array", `
For Each x In Array("a", "b", "c")
  s = s & x
Next
WScript.Echo s`, "abc"},
		{"for each dictionary keys", `
Set d = CreateObject("Scripting.Dictionary")
d.Add "one", 1 : d.Add "two", 2
For Each k In d
  WScript.Echo k, d(k)
Next
For Each k In d.Keys : s = s & k : Next
For Each v In d.Items : n = n + v : Next
WScript.Echo s, n`, "one 1\ntwo 2\nonetwo 3"},
		{"for each exit and multi-dim", `
Dim m(1, 1)
m(0, 0) = 1 : m(1, 0) = 2 : m(0, 1) = 3 : m(1, 1) = 4
For Each v In m
  s = s & v
  If v = 3 Then Exit For
Next
WScript.Echo s`, "123"},
		{"for each over empty array", `
For Each x In Array()
  WScript.Echo "never"
Next
WScript.Echo "ok"`, "ok"},
		{"do while", `
i = 0
Do While i < 3
  i = i + 1
Loop
WScript.Echo i`, "3"},
		{"do until", `
i = 0
Do Until i >= 3
  i = i + 1
Loop
WScript.Echo i`, "3"},
		{"do loop while", `
i = 10
Do
  i = i + 1
Loop While i < 3
WScript.Echo i`, "11"},
		{"do loop until", `
i = 0
Do
  i = i + 1
Loop Until i = 4
WScript.Echo i`, "4"},
		{"do exit", `
i = 0
Do
  i = i + 1
  If i = 7 Then Exit Do
Loop
WScript.Echo i`, "7"},
		{"while wend", `
i = 0
While i < 4
  i = i + 1
Wend
WScript.Echo i`, "4"},
		{"exit do inside for inside do", `
Do
  For i = 1 To 3
    If i = 2 Then Exit Do
  Next
  WScript.Echo "not reached"
Loop
WScript.Echo "out", i`, "out 2"},
		{"single line loops", `
For i = 1 To 3 : s = s & i : Next
Do While Len(s) < 6 : s = s & "x" : Loop
WScript.Echo s`, "123xxx"},
	})
}

func TestProcedures(t *testing.T) {
	runCases(t, []tcase{
		{"sub and function", `
Sub Greet(name)
  WScript.Echo "Hello, " & name
End Sub
Function Add(a, b)
  Add = a + b
End Function
Greet "World"
Call Greet("Call")
Greet("Parens")
WScript.Echo Add(2, 3)`, "Hello, World\nHello, Call\nHello, Parens\n5"},
		{"called before definition", `
WScript.Echo Twice(4)
Function Twice(n) : Twice = n * 2 : End Function`, "8"},
		{"byref is the default", `
Sub Inc(n)
  n = n + 1
End Sub
x = 1
Inc x
Call Inc(x)
WScript.Echo x`, "3"},
		{"byval and parenthesised arguments", `
Sub IncVal(ByVal n) : n = n + 1 : End Sub
Sub IncRef(ByRef n) : n = n + 1 : End Sub
x = 1
IncVal x
WScript.Echo x
IncRef (x)
WScript.Echo x
IncRef x
WScript.Echo x
Call IncRef((x))
WScript.Echo x`, "1\n1\n2\n2"},
		{"byref array element and whole array", `
Sub Inc(n) : n = n + 1 : End Sub
Sub Fill(arr) : arr(0) = "filled" : End Sub
Dim a(1)
a(0) = 5
Inc a(0)
WScript.Echo a(0)
Fill a
WScript.Echo a(0)`, "6\nfilled"},
		{"byref swap and multiple results", `
Sub Swap(a, b)
  Dim t : t = a : a = b : b = t
End Sub
Function DivMod(n, d, ByRef remainder)
  remainder = n Mod d
  DivMod = n \ d
End Function
p = 1 : q = 2
Swap p, q
WScript.Echo p, q
WScript.Echo DivMod(17, 5, r), r`, "2 1\n3 2"},
		{"byref creates undeclared variable", `
Sub SetIt(v) : v = "set" : End Sub
SetIt fresh
WScript.Echo fresh`, "set"},
		{"recursion", `
Function Fact(n)
  If n <= 1 Then
    Fact = 1
  Else
    Fact = n * Fact(n - 1)
  End If
End Function
Function Fib(n)
  If n < 2 Then Fib = n Else Fib = Fib(n - 1) + Fib(n - 2)
End Function
WScript.Echo Fact(10), Fib(15)`, "3628800 610"},
		{"function name as accumulator", `
Function Stars(n)
  Dim i
  For i = 1 To n
    Stars = Stars & "*"
  Next
End Function
WScript.Echo Stars(4), "[" & Stars(0) & "]"`, "**** []"},
		{"exit function and exit sub", `
Function First(arr)
  For Each x In arr
    First = x
    Exit Function
  Next
  First = "none"
End Function
Sub S
  WScript.Echo "a"
  Exit Sub
  WScript.Echo "b"
End Sub
S
WScript.Echo First(Array(7, 8)), First(Array())`, "a\n7 none"},
		{"locals shadow globals", `
x = "global"
Sub P
  Dim x
  x = "local"
  y = "created local"
End Sub
Sub Q
  x = "changed by Q"
End Sub
P
WScript.Echo x, IsEmpty(y)
Q
WScript.Echo x`, "global True\nchanged by Q"},
		{"function returning array and object", `
Function Pair(a, b) : Pair = Array(a, b) : End Function
Function NewDict
  Set NewDict = CreateObject("Scripting.Dictionary")
  NewDict.Add "k", "v"
End Function
p = Pair(1, 2)
WScript.Echo p(1), Pair("x", "y")(0), NewDict()("k"), NewDict.Count`, "2 x v 1"},
		{"call statement forms", `
Sub Show(a, b) : WScript.Echo a & "/" & b : End Sub
Sub One(a) : WScript.Echo "one:" & a : End Sub
Sub None : WScript.Echo "none" : End Sub
Show 1, 2
Call Show(3, 4)
Show (5), 6
Show (5) + 1, (7)
One (8) * 2
One(9)
One -1
None
Call None
Call None()
None()`, "1/2\n3/4\n5/6\n6/7\none:16\none:9\none:-1\nnone\nnone\nnone\nnone"},
		{"function called as statement discards result", `
Function F(x) : WScript.Echo "F" & x : F = x : End Function
F 1
F(2)
Call F(3)
y = F(4)`, "F1\nF2\nF3\nF4"},
		{"private and public procedures", `
Private Sub Hidden : WScript.Echo "hidden" : End Sub
Public Function Shown : Shown = "shown" : End Function
Hidden
WScript.Echo Shown`, "hidden\nshown"},
		{"array parameter syntax", `
Function Total(values())
  Dim v
  For Each v In values : Total = Total + v : Next
End Function
WScript.Echo Total(Array(1, 2, 3))`, "6"},
		{"getref", `
Function Sq(x) : Sq = x * x : End Function
Sub Hello : WScript.Echo "hello" : End Sub
Set f = GetRef("Sq")
WScript.Echo f(7)
Set h = GetRef("Hello")
h
Call h()`, "49\nhello\nhello"},
		{"deep recursion", `
Function Depth(n)
  If n = 0 Then Depth = 0 Else Depth = 1 + Depth(n - 1)
End Function
WScript.Echo Depth(1000)`, "1000"},
	})
}

func TestWith(t *testing.T) {
	runCases(t, []tcase{
		{"dictionary", `
Set d = CreateObject("Scripting.Dictionary")
With d
  .Add "a", 1
  .Add "b", 2
  .Item("a") = 10
  WScript.Echo .Count, .Item("a"), .Exists("b")
End With`, "2 10 True"},
		{"nested and class", `
Class Box
  Public Value
  Public Inner
End Class
Set b = New Box
Set b.Inner = New Box
With b
  .Value = 1
  With .Inner
    .Value = 2
    WScript.Echo .Value
  End With
  WScript.Echo .Value + .Inner.Value
End With`, "2\n3"},
		{"call with member argument", `
Class Box
  Public Value
End Class
Sub Show(v) : WScript.Echo "v=" & v : End Sub
Set b = New Box
b.Value = 9
With b
  Show .Value
  If .Value = 9 Then WScript.Echo "nine"
  x = .Value + 1
End With
WScript.Echo x`, "v=9\nnine\n10"},
	})
}

func TestExecuteAndEval(t *testing.T) {
	runCases(t, []tcase{
		{"eval", `
x = 4
WScript.Echo Eval("x * 2 + 1"), Eval("x = 4"), Eval("""a"" & ""b""")`, "9 True ab"},
		{"execute", `
Execute "y = 6 : z = y + 1"
WScript.Echo y, z
Execute "Sub Dyn : WScript.Echo ""dynamic"" : End Sub"
Dyn`, "6 7\ndynamic"},
		{"execute sees locals, executeglobal does not", `
Sub Test
  Dim loc
  loc = 1
  Execute "loc = loc + 1"
  ExecuteGlobal "glob = 5"
  WScript.Echo loc
End Sub
Test
WScript.Echo glob`, "2\n5"},
		{"executeglobal defines functions", `
ExecuteGlobal "Function Triple(n)" & vbCrLf & "Triple = n * 3" & vbCrLf & "End Function"
WScript.Echo Triple(5)`, "15"},
		{"eval error is trappable", `
On Error Resume Next
x = Eval("1 +")
WScript.Echo Err.Number <> 0
Err.Clear
Execute "this is not valid("
WScript.Echo Err.Number <> 0`, "True\nTrue"},
	})
}

func TestLexicalForms(t *testing.T) {
	runCases(t, []tcase{
		{"comments", `
' a comment
x = 1 ' trailing comment
Rem another comment
x = x + 1 : Rem trailing rem
REM x = 100
WScript.Echo x`, "2"},
		{"line continuation", `
x = 1 + _
    2 + _
    3
WScript.Echo _
  x, _
  "ok"
s = "a" & _
"b"
WScript.Echo s`, "6 ok\nab"},
		{"statement separator", `x = 1 : y = 2 : WScript.Echo x + y : : WScript.Echo "z"`, "3\nz"},
		{"case insensitivity", `
DIM Counter
counter = 1
COUNTER = Counter + 1
wscript.ECHO counter, lcase("ABC"), VBCRLF = vbcrlf
iF TRUE tHEN WScript.Echo "kw"`, "2 abc True\nkw"},
		{"host directives are ignored", "# $language = \"VBScript\"\n# $interface = \"1.0\"\n\n' comment\nWScript.Echo \"line 5\"", "line 5"},
		{"string escapes", `
WScript.Echo "say ""hi""", Len(""""), "'not a comment'"`, `say "hi" 1 'not a comment'`},
		{"crlf line endings", "x = 1\r\ny = 2\r\nWScript.Echo x + y\r\n", "3"},
		{"numeric literal forms", `
WScript.Echo &HFF, &H10 + 1, &O17, 1.5E2, 1E3, .5 + .5, 1e-2, &HFFFF, &HFFFF&, &HFFFFFFFF, 2.`, "255 17 15 150 1000 1 0.01 -1 65535 -1 2"},
		{"date literals", `
WScript.Echo #1/2/2020#, #2020-03-04#, #1/2/2020 3:04:05 PM#, #12:30:00 PM#, #March 5, 2021#`,
			"1/2/2020 3/4/2020 1/2/2020 3:04:05 PM 12:30:00 PM 3/5/2021"},
		{"keywords as member names", `
Class K
  Public Function [End] : [End] = "end" : End Function
  Public Property Get [Next] : [Next] = "next" : End Property
End Class
Set k = New K
WScript.Echo k.End, k.Next`, "end next"},
		{"ampersand before identifier is concatenation", `
header = "H" : face = "F"
s = "x" &header &face
WScript.Echo s`, "xHF"},
		{"empty parens and omitted arguments", `
Function Two(a, b) : Two = "[" & a & "|" & b & "]" : End Function
WScript.Echo Two(, 2), Two(1, ), IsEmpty(Eval("Empty"))`, "[|2] [1|] True"},
	})
}

func TestMiscStatements(t *testing.T) {
	runCases(t, []tcase{
		{"randomize and rnd", `
Randomize
r = Rnd
WScript.Echo r >= 0 And r < 1
Randomize 42
a = Rnd : b = Rnd
WScript.Echo a <> b, Int(Rnd * 6) + 1 <= 6`, "True\nTrue True"},
		{"rnd default sequence", `
WScript.Echo Rnd, Rnd(0) = Rnd(0)
a = Rnd(-1)
b = Rnd(-1)
WScript.Echo a = b`, "0.7055475 True\nTrue"},
		{"stop is a no-op", `
Stop
WScript.Echo "ok"`, "ok"},
		{"default member of object in value context", `
Set re = New RegExp
re.Pattern = "\d+"
Set m = re.Execute("abc 123")
s = m(0)
WScript.Echo s, m(0) & "!", Len(m(0)), TypeName(s)`, "123 123! 3 String"},
	})
}
