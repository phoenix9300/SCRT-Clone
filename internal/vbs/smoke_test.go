package vbs

import (
	"bytes"
	"context"
	"testing"
)

func TestSmoke(t *testing.T) {
	src := `
Option Explicit
Dim i, s
s = ""
For i = 1 To 5
  s = s & i & ","
Next
WScript.Echo s
WScript.Echo 1/3, 2^10, "5" + 3, 7 \ 2, 7 Mod 3, 1E+20, 0.1 + 0.2
Function Fact(n)
  If n <= 1 Then Fact = 1 Else Fact = n * Fact(n - 1)
End Function
WScript.Echo Fact(10), Rnd
Dim d : Set d = CreateObject("Scripting.Dictionary")
d.Add "a", 1 : d("b") = 2
Dim k
For Each k In d : WScript.Echo k, d(k) : Next
Class P
  Private m
  Public Property Get V : V = m : End Property
  Public Property Let V(x) : m = x : End Property
  Public Default Function Hi(n) : Hi = "hi " & n & m : End Function
End Class
Dim p : Set p = New P
p.V = 42
WScript.Echo p.V, p("x"), TypeName(p)
On Error Resume Next
i = 1 / 0
WScript.Echo Err.Number, Err.Description
`
	in := New()
	var buf bytes.Buffer
	in.SetOutput(&buf)
	err := in.Run(context.Background(), src, "smoke.vbs")
	t.Logf("err=%v\n%s", err, buf.String())
}
