package vbs

import "testing"

func TestArrayBuiltins(t *testing.T) {
	runCases(t, []tcase{
		{"array basics", `
a = Array(10, 20, 30)
WScript.Echo UBound(a), LBound(a), a(1), IsArray(a), IsArray(5), UBound(Array())`, "2 0 20 True False -1"},
		{"multi dim", `
Dim m(2, 3)
m(1, 2) = "x"
WScript.Echo UBound(m, 1), UBound(m, 2), LBound(m, 2), m(1, 2), IsEmpty(m(0, 0))`, "2 3 0 x True"},
		{"redim", `
Dim a
ReDim a(2)
a(0) = 1 : a(2) = 3
ReDim a(4)
WScript.Echo UBound(a), IsEmpty(a(0))`, "4 True"},
		{"redim preserve", `
Dim a()
ReDim a(1)
a(0) = "p" : a(1) = "q"
ReDim Preserve a(3)
a(3) = "s"
WScript.Echo UBound(a), a(0), a(1), IsEmpty(a(2)), a(3)`, "3 p q True s"},
		{"redim preserve multi last dim", `
Dim a()
ReDim a(1, 1)
a(1, 1) = 5
ReDim Preserve a(1, 3)
WScript.Echo UBound(a, 2), a(1, 1)`, "3 5"},
		{"erase fixed", `
Dim a(2)
a(1) = 5
Erase a
WScript.Echo UBound(a), IsEmpty(a(1))`, "2 True"},
		{"erase dynamic", `
Dim a()
ReDim a(2)
Erase a
On Error Resume Next
x = UBound(a)
WScript.Echo Err.Number`, "9"},
		{"for each array", `
For Each x In Array(1, 2, 3)
  s = s & x
Next
WScript.Echo s`, "123"},
		{"for each empty array", `
For Each x In Array()
  s = s & x
Next
WScript.Echo "[" & s & "]"`, "[]"},
		{"for each multi dim", `
Dim m(1, 1)
m(0, 0) = 1 : m(0, 1) = 2 : m(1, 0) = 3 : m(1, 1) = 4
For Each x In m
  s = s & x
Next
WScript.Echo s`, "1324"},
		{"for each modify copy", `
a = Array(1, 2)
For Each x In a
  x = x * 10
Next
WScript.Echo a(0), a(1)`, "1 2"},
		{"array copy semantics", `
a = Array(1, 2)
b = a
b(0) = 99
WScript.Echo a(0), b(0)`, "1 99"},
		{"nested arrays", `
a = Array(Array(1, 2), Array(3, 4))
WScript.Echo a(1)(0), UBound(a(0))`, "3 1"},
		{"array of mixed", `
a = Array(1, "b", True, Null)
WScript.Echo TypeName(a(0)), TypeName(a(1)), TypeName(a(2)), TypeName(a(3)), TypeName(a)`, "Integer String Boolean Null Variant()"},
		{"dim array typename", `Dim a(2) : WScript.Echo TypeName(a), VarType(a)`, "Variant() 8204"},
		{"ubound dimension", `
Dim m(1, 2, 3)
WScript.Echo UBound(m, 3)`, "3"},
		{"array element byref", `
Sub Inc(ByRef n) : n = n + 1 : End Sub
Dim a(1) : a(0) = 1
Inc a(0)
WScript.Echo a(0)`, "2"},
		{"split join roundtrip", `
a = Split("x;y;z", ";")
a(1) = "Y"
WScript.Echo Join(a, ";"), UBound(a)`, "x;Y;z 2"},
		{"array as parameter by ref", `
Sub Fill(arr) : arr(0) = "f" : End Sub
Dim a(1)
Fill a
WScript.Echo a(0)`, "f"},
	})
	runErrCases(t, []errCase{
		{"out of range", "Dim a(2)\na(3) = 1", 9, 2, false},
		{"negative index", "Dim a(2)\nx = a(-1)", 9, 2, false},
		{"not an array", "x = 5\ny = x(1)", 13, 2, false},
		{"ubound non-array", "x = UBound(5)", 13, 1, false},
		{"fixed redim", "Dim a(2)\nReDim a(3)", 10, 2, false},
	})
}

func TestTypeTests(t *testing.T) {
	evalCases(t, []ecase{
		{`IsEmpty(Empty)`, "True"},
		{`IsEmpty(0)`, "False"},
		{`IsEmpty("")`, "False"},
		{`IsNull(Null)`, "True"},
		{`IsNull("")`, "False"},
		{`IsNumeric(5)`, "True"},
		{`IsNumeric("5")`, "True"},
		{`IsNumeric("5.5")`, "True"},
		{`IsNumeric(" 5 ")`, "True"},
		{`IsNumeric("&H10")`, "True"},
		{`IsNumeric("1e3")`, "True"},
		{`IsNumeric("abc")`, "False"},
		{`IsNumeric("")`, "False"},
		{`IsNumeric(Empty)`, "True"},
		{`IsNumeric(Null)`, "False"},
		{`IsNumeric(True)`, "True"},
		{`IsNumeric("1,000")`, "True"},
		{`IsNumeric("5a")`, "False"},
		{`IsNumeric(Array(1))`, "False"},
		{`IsDate("2020-03-04")`, "True"},
		{`IsDate("3/4/2020")`, "True"},
		{`IsDate("13:45")`, "True"},
		{`IsDate("hello")`, "False"},
		{`IsDate(5)`, "False"},
		{`IsDate(#1/1/2000#)`, "True"},
		{`IsDate("2/30/2020")`, "False"},
		{`IsObject(Nothing)`, "True"},
		{`IsObject(5)`, "False"},
		{`IsObject(CreateObject("Scripting.Dictionary"))`, "True"},
		{`VarType(Empty)`, "0"},
		{`VarType(Null)`, "1"},
		{`VarType(5)`, "2"},
		{`VarType(40000)`, "3"},
		{`VarType(1.5)`, "5"},
		{`VarType(#1/1/2000#)`, "7"},
		{`VarType("a")`, "8"},
		{`VarType(Nothing)`, "9"},
		{`VarType(True)`, "11"},
		{`VarType(Array(1))`, "8204"},
		{`VarType(CreateObject("Scripting.Dictionary"))`, "9"},
		{`TypeName(Empty)`, "Empty"},
		{`TypeName(Null)`, "Null"},
		{`TypeName(5)`, "Integer"},
		{`TypeName(70000)`, "Long"},
		{`TypeName(1.5)`, "Double"},
		{`TypeName("a")`, "String"},
		{`TypeName(True)`, "Boolean"},
		{`TypeName(Nothing)`, "Nothing"},
		{`TypeName(Array(1))`, "Variant()"},
		{`TypeName(CreateObject("Scripting.Dictionary"))`, "Dictionary"},
		{`TypeName(CreateObject("Scripting.FileSystemObject"))`, "FileSystemObject"},
		{`TypeName(CreateObject("VBScript.RegExp"))`, "IRegExp2"},
		{`TypeName(CreateObject("WScript.Shell"))`, "IWshShell3"},
		{`TypeName(Err)`, "ErrObject"},
	})
	runCases(t, []tcase{
		{"typename of class", `
Class Foo : End Class
Dim f : Set f = New Foo
WScript.Echo TypeName(f), VarType(f), IsObject(f)`, "Foo 9 True"},
	})
}

func TestConstants(t *testing.T) {
	evalCases(t, []ecase{
		{`vbCr`, "\r"},
		{`vbLf`, "\n"},
		{`vbCrLf`, "\r\n"},
		{`vbNewLine`, "\r\n"},
		{`vbTab`, "\t"},
		{`vbNullChar`, "\x00"},
		{`vbBack`, "\b"},
		{`vbFormFeed`, "\f"},
		{`vbVerticalTab`, "\v"},
		{`Len(vbNullString)`, "0"},
		{`vbBinaryCompare`, "0"},
		{`vbTextCompare`, "1"},
		{`vbEmpty`, "0"},
		{`vbNull`, "1"},
		{`vbInteger`, "2"},
		{`vbLong`, "3"},
		{`vbSingle`, "4"},
		{`vbDouble`, "5"},
		{`vbCurrency`, "6"},
		{`vbDate`, "7"},
		{`vbString`, "8"},
		{`vbObject`, "9"},
		{`vbError`, "10"},
		{`vbBoolean`, "11"},
		{`vbVariant`, "12"},
		{`vbDataObject`, "13"},
		{`vbDecimal`, "14"},
		{`vbByte`, "17"},
		{`vbArray`, "8192"},
		{`vbSunday`, "1"},
		{`vbMonday`, "2"},
		{`vbSaturday`, "7"},
		{`vbUseSystemDayOfWeek`, "0"},
		{`vbFirstJan1`, "1"},
		{`vbFirstFourDays`, "2"},
		{`vbFirstFullWeek`, "3"},
		{`vbGeneralDate`, "0"},
		{`vbLongDate`, "1"},
		{`vbShortDate`, "2"},
		{`vbLongTime`, "3"},
		{`vbShortTime`, "4"},
		{`vbOKOnly`, "0"},
		{`vbOKCancel`, "1"},
		{`vbYesNo`, "4"},
		{`vbCritical`, "16"},
		{`vbQuestion`, "32"},
		{`vbExclamation`, "48"},
		{`vbInformation`, "64"},
		{`vbOK`, "1"},
		{`vbCancel`, "2"},
		{`vbYes`, "6"},
		{`vbNo`, "7"},
		{`vbTrue`, "-1"},
		{`vbFalse`, "0"},
		{`vbUseDefault`, "-2"},
		{`vbObjectError`, "-2147221504"},
	})
}
