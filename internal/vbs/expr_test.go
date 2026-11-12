package vbs

import "testing"

func TestPrecedence(t *testing.T) {
	evalCases(t, []ecase{
		{"2 + 3 * 4", "14"},
		{"(2 + 3) * 4", "20"},
		{"2 ^ 3 ^ 2", "64"},
		{"-2 ^ 2", "-4"},
		{"2 ^ -1", "0.5"},
		{"- - 3", "3"},
		{"10 - 4 - 3", "3"},
		{"2 * 3 Mod 4", "2"},
		{"10 \\ 3 * 2", "1"},
		{"7 Mod 3 + 1", "2"},
		{"6 / 4", "1.5"},
		{"7 \\ 2", "3"},
		{"1 + 2 & 3", "33"},
		{"1 & 2 + 3", "15"},
		{"1 + 1 = 2", "True"},
		{"\"a\" & \"b\" = \"ab\"", "True"},
		{"Not 1 = 2", "True"},
		{"Not True And False", "False"},
		{"True Or False And False", "True"},
		{"True And False Or True", "True"},
		{"True Xor True", "False"},
		{"True Xor False", "True"},
		{"False Eqv False", "True"},
		{"True Eqv False", "False"},
		{"False Imp True", "True"},
		{"True Imp False", "False"},
		{"True Imp True", "True"},
		{"False Imp False", "True"},
		{"5 And 3", "1"},
		{"5 Or 3", "7"},
		{"5 Xor 3", "6"},
		{"Not 5", "-6"},
		{"Not 0", "-1"},
		{"1 < 2 And 2 < 3", "True"},
		{"1 <> 2", "True"},
		{"2 >= 2", "True"},
		{"2 <= 1", "False"},
		{"-3 ^ 2", "-9"},
		{"2 * -3", "-6"},
		{"1 - -1", "2"},
	})
}

func TestIsOperator(t *testing.T) {
	runCases(t, []tcase{
		{"is", `
Dim a, b, c
Set a = CreateObject("Scripting.Dictionary")
Set b = a
Set c = CreateObject("Scripting.Dictionary")
WScript.Echo a Is b, a Is c, a Is Nothing, c Is Nothing`, "True False False False"},
		{"nothing is nothing", `
Dim a : Set a = Nothing
WScript.Echo a Is Nothing`, "True"},
	})
}

func TestCoercion(t *testing.T) {
	evalCases(t, []ecase{
		{`"5" + 3`, "8"},
		{`"5" & 3`, "53"},
		{`"a" & 1`, "a1"},
		{`1 & 2`, "12"},
		{`"5" * "4"`, "20"},
		{`"10" / 4`, "2.5"},
		{`1 = "1"`, "True"},
		{`"1" = 1`, "True"},
		{`"abc" = "ABC"`, "False"},
		{`"abc" < "abd"`, "True"},
		{`"a" < "B"`, "False"},
		{`"10" < "9"`, "True"},
		{`10 < "9"`, "False"},
		{`True + 1`, "0"},
		{`True & ""`, "True"},
		{`-True`, "1"},
		{`CInt(True)`, "-1"},
		{`1.5 & ""`, "1.5"},
		{`0.1 + 0.2 = 0.3`, "False"},
		{`1 / 3`, "0.333333333333333"},
		{`2 ^ 0.5`, "1.4142135623731"},
		{`10 / 2`, "5"},
		{`1E3`, "1000"},
		{`1.5E-3`, "0.0015"},
		{`1E20`, "1E+20"},
		{`123456789012`, "123456789012"},
		{`100000 * 100000`, "10000000000"},
		{`"3" + "4"`, "34"},
		{`"3" & "4"`, "34"},
		{`" 12 " + 1`, "13"},
		{`CStr(1.0)`, "1"},
		{`Empty + 1`, "1"},
		{`Empty & "x"`, "x"},
		{`Empty = 0`, "True"},
		{`Empty = ""`, "True"},
	})
	runErrCases(t, []errCase{
		{"string plus number", "x = \"abc\" + 1", 13, 1, false},
		{"mul string", "x = \"abc\" * 2", 13, 1, false},
		{"object arithmetic", "Set d = CreateObject(\"Scripting.Dictionary\")\nx = d + 1", 438, 2, false},
	})
}

func TestNullEmptyNothing(t *testing.T) {
	runCases(t, []tcase{
		{"null propagation", `
WScript.Echo IsNull(Null + 1), IsNull(Null & "a"), IsNull(Null = 1), IsNull(Null * 2)
WScript.Echo "[" & Null & "a]"`, "True False True True\n[a]"},
		{"null logic", `
WScript.Echo IsNull(Null And True), Null And False, Null Or True, IsNull(Null Or False)
WScript.Echo IsNull(Not Null)`, "True False True True\nTrue"},
		{"null in if", `
x = Null
If x Then WScript.Echo "t" Else WScript.Echo "f"`, "f"},
		{"empty", `
Dim e
WScript.Echo IsEmpty(e), e + 1, "[" & e & "]", VarType(e), TypeName(e), e = 0, e = ""`, "True 1 [] 0 Empty True True"},
		{"nothing", `
Dim o
Set o = Nothing
WScript.Echo TypeName(o), VarType(o), o Is Nothing`, "Nothing 9 True"},
		{"null typename", `WScript.Echo TypeName(Null), VarType(Null)`, "Null 1"},
		{"member of nothing", `
On Error Resume Next
Dim o : Set o = Nothing
o.Foo
WScript.Echo Err.Number`, "91"},
		{"member of empty", `
On Error Resume Next
Dim o
o.Foo
WScript.Echo Err.Number`, "424"},
	})
	runErrCases(t, []errCase{
		{"null to cstr", "x = CStr(Null)", 94, 1, false},
	})
}

func TestLiterals(t *testing.T) {
	evalCases(t, []ecase{
		{"&HFF", "255"},
		{"&hff", "255"},
		{"&H10", "16"},
		{"&HFFFF", "-1"},
		{"&H7FFF", "32767"},
		{"&H10000", "65536"},
		{"&HFFFFFFFF", "-1"},
		{"&O17", "15"},
		{"&O777", "511"},
		{"1e2", "100"},
		{"2.5e+2", "250"},
		{".5", "0.5"},
		{"5.", "5"},
		{`"say ""hi"""`, `say "hi"`},
		{`""`, ""},
		{"Year(#2020-03-04#)", "2020"},
		{"Month(#3/4/2020#)", "3"},
		{"Day(#3/4/2020#)", "4"},
		{"Hour(#3/4/2020 5:06:07 PM#)", "17"},
		{"Minute(#3/4/2020 5:06:07 PM#)", "6"},
		{"Second(#3/4/2020 5:06:07 PM#)", "7"},
		{"Hour(#13:45:00#)", "13"},
		{"True", "True"},
		{"False", "False"},
		{"vbCrLf = Chr(13) & Chr(10)", "True"},
		{"TypeName(&HFF)", "Integer"},
		{"TypeName(&H10000)", "Long"},
		{"TypeName(1.5)", "Double"},
		{"TypeName(1E3)", "Double"},
		{`TypeName("s")`, "String"},
		{"TypeName(#1/1/2000#)", "Date"},
		{"TypeName(True)", "Boolean"},
		{"TypeName(40000)", "Long"},
		{"TypeName(3)", "Integer"},
	})
}

func TestIntegerArithmetic(t *testing.T) {
	evalCases(t, []ecase{
		{`7 \ 2`, "3"},
		{`-7 \ 2`, "-3"},
		{`7 \ -2`, "-3"},
		{`-7 \ -2`, "3"},
		{`7.6 \ 2`, "4"},
		{`7 \ 2.5`, "3"},
		{`7 Mod 3`, "1"},
		{`-7 Mod 3`, "-1"},
		{`7 Mod -3`, "1"},
		{`-7 Mod -3`, "-1"},
		{`7.5 Mod 2`, "0"},
		{`8.5 Mod 3`, "2"},
		{`2.5 Mod 2`, "0"},
		{`3.5 Mod 2`, "0"},
		{`"7" Mod 3`, "1"},
		{`1 / 4`, "0.25"},
		{`CLng(2147483647) + 0`, "2147483647"},
	})
}

func TestArithmeticErrors(t *testing.T) {
	runErrCases(t, []errCase{
		{"div zero", "x = 1\ny = 5 / (x - 1)", 11, 2, false},
		{"idiv zero", "y = 5 \\ 0", 11, 1, false},
		{"mod zero", "y = 5 Mod 0", 11, 1, false},
		{"float div zero", "y = 5.5 / 0", 11, 1, false},
		{"zero over zero", "y = 0 / 0", 11, 1, false},
		{"cint overflow", "y = CInt(40000)", 6, 1, false},
		{"cint overflow neg", "y = CInt(-40000)", 6, 1, false},
		{"clng overflow", "y = CLng(3000000000)", 6, 1, false},
		{"cbyte overflow", "y = CByte(256)", 6, 1, false},
		{"cint string", "y = CInt(\"abc\")", 13, 1, false},
		{"pow zero neg", "y = 0 ^ -1", 11, 1, false},
		{"hex overflow", "y = Hex(\"zz\")", 13, 1, false},
	})
	runCases(t, []tcase{
		{"div zero trapped", `
On Error Resume Next
x = 1 / 0
WScript.Echo Err.Number, Err.Description`, "11 Division by zero"},
		{"large mul becomes double", `
x = 2000000000 * 2
WScript.Echo x, TypeName(x)`, "4000000000 Double"},
		{"int mul stays int", `
x = 300 * 300
WScript.Echo x, TypeName(x)`, "90000 Long"},
		{"integer overflow add", `
Dim a : a = CInt(32767)
On Error Resume Next
b = a + 1
WScript.Echo Err.Number, b`, "0 32768"},
	})
}
