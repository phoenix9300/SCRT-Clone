package vbs

import "testing"

func TestDictionary(t *testing.T) {
	runCases(t, []tcase{
		{"basic", `
Set d = CreateObject("Scripting.Dictionary")
d.Add "a", 1
d.Add "b", "two"
d("c") = 3
d.Item("a") = 10
WScript.Echo d.Count, d("a"), d.Item("b"), d.Exists("c"), d.Exists("z"), d.Exists("A")`, "3 10 two True False False"},
		{"missing key creates empty", `
Set d = CreateObject("Scripting.Dictionary")
x = d("nope")
WScript.Echo IsEmpty(x), d.Count, d.Exists("nope")`, "True 1 True"},
		{"keys items", `
Set d = CreateObject("Scripting.Dictionary")
d.Add "x", 1 : d.Add "y", 2 : d.Add "z", 3
k = d.Keys : i = d.Items
WScript.Echo Join(k, ","), Join(i, ","), UBound(k), TypeName(k)`, "x,y,z 1,2,3 2 Variant()"},
		{"remove and removeall", `
Set d = CreateObject("Scripting.Dictionary")
d.Add "x", 1 : d.Add "y", 2
d.Remove "x"
WScript.Echo d.Count, d.Exists("x")
d.RemoveAll
WScript.Echo d.Count`, "1 False\n0"},
		{"key rename", `
Set d = CreateObject("Scripting.Dictionary")
d.Add "old", 1
d.Key("old") = "new"
WScript.Echo d.Exists("old"), d("new")`, "False 1"},
		{"compare mode text", `
Set d = CreateObject("Scripting.Dictionary")
d.CompareMode = vbTextCompare
d.Add "Key", 1
WScript.Echo d.Exists("KEY"), d("kEy"), d.CompareMode`, "True 1 1"},
		{"compare mode default binary", `
Set d = CreateObject("Scripting.Dictionary")
WScript.Echo d.CompareMode`, "0"},
		{"for each iterates keys", `
Set d = CreateObject("Scripting.Dictionary")
d.Add "a", 1 : d.Add "b", 2
For Each k In d
  s = s & k & "=" & d(k) & ";"
Next
WScript.Echo s`, "a=1;b=2;"},
		{"for each empty dict", `
Set d = CreateObject("Scripting.Dictionary")
For Each k In d : n = n + 1 : Next
WScript.Echo IsEmpty(n)`, "True"},
		{"numeric keys", `
Set d = CreateObject("Scripting.Dictionary")
d.Add 1, "one" : d.Add 2, "two"
WScript.Echo d(1), d(2), d.Exists(3)`, "one two False"},
		{"object values", `
Set d = CreateObject("Scripting.Dictionary")
Set e = CreateObject("Scripting.Dictionary")
e.Add "inner", 5
d.Add "e", e
WScript.Echo d("e")("inner"), d.Item("e").Count`, "5 1"},
		{"set item to object", `
Set d = CreateObject("Scripting.Dictionary")
Set d("o") = CreateObject("Scripting.Dictionary")
WScript.Echo TypeName(d("o"))`, "Dictionary"},
		{"insertion order after remove", `
Set d = CreateObject("Scripting.Dictionary")
d.Add "a", 1 : d.Add "b", 2 : d.Add "c", 3
d.Remove "b"
d.Add "b", 4
WScript.Echo Join(d.Keys, "")`, "acb"},
		{"typename", `Set d = CreateObject("Scripting.Dictionary") : WScript.Echo TypeName(d)`, "Dictionary"},
		{"case insensitive progid", `Set d = CreateObject("scripting.dictionary") : WScript.Echo d.Count`, "0"},
		{"new dictionary on dim", `
Dim d : Set d = CreateObject("Scripting.Dictionary")
d.Add "k", Array(1, 2)
WScript.Echo d("k")(1)`, "2"},
	})
	runErrCases(t, []errCase{
		{"duplicate key", "Set d = CreateObject(\"Scripting.Dictionary\")\nd.Add \"a\", 1\nd.Add \"a\", 2", 457, 3, false},
		{"remove missing", "Set d = CreateObject(\"Scripting.Dictionary\")\nd.Remove \"zz\"", 32811, 2, false},
		{"compare mode when non-empty", "Set d = CreateObject(\"Scripting.Dictionary\")\nd.Add \"a\", 1\nd.CompareMode = 1", 5, 3, false},
		{"key missing", "Set d = CreateObject(\"Scripting.Dictionary\")\nd.Key(\"zz\") = 1", 32811, 2, false},
	})
}

func TestRegExp(t *testing.T) {
	runCases(t, []tcase{
		{"test", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "^\d+$"
WScript.Echo re.Test("123"), re.Test("12a"), re.Test("")`, "True False False"},
		{"ignore case", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "hello"
WScript.Echo re.Test("HELLO")
re.IgnoreCase = True
WScript.Echo re.Test("HELLO"), re.IgnoreCase`, "False\nTrue True"},
		{"replace first only by default", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "a"
WScript.Echo re.Replace("banana", "o")`, "bonana"},
		{"replace global", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "a" : re.Global = True
WScript.Echo re.Replace("banana", "o"), re.Global`, "bonono True"},
		{"replace with groups", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "(\w+)@(\w+)" : re.Global = True
WScript.Echo re.Replace("joe@home and amy@work", "$2:$1")`, "home:joe and work:amy"},
		{"replace dollar ampersand", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "\d+"
WScript.Echo re.Replace("a12b", "[$&]")`, "a[12]b"},
		{"replace literal dollar", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "x"
WScript.Echo re.Replace("axb", "$$")`, "a$b"},
		{"execute matches", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "(\d+)-(\d+)" : re.Global = True
Set ms = re.Execute("10-20 and 30-40")
WScript.Echo ms.Count, ms(0).Value, ms(1).Value, ms(0).FirstIndex, ms(1).FirstIndex, ms(0).Length
WScript.Echo ms(0).SubMatches(0), ms(0).SubMatches(1), ms(1).SubMatches.Count, ms(1).SubMatches(1)`, "2 10-20 30-40 0 10 5\n10 20 2 40"},
		{"execute no match", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "z"
Set ms = re.Execute("abc")
WScript.Echo ms.Count`, "0"},
		{"execute non global gives one", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "\d"
WScript.Echo re.Execute("1 2 3").Count`, "1"},
		{"for each over matches", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "\w+" : re.Global = True
For Each m In re.Execute("ab cd ef")
  s = s & m.Value & "|"
Next
WScript.Echo s`, "ab|cd|ef|"},
		{"for each over submatches", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "(a)(b)(c)"
For Each m In re.Execute("abc")(0).SubMatches
  s = s & m
Next
WScript.Echo s`, "abc"},
		{"multiline", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "^b$"
WScript.Echo re.Test("a" & vbLf & "b" & vbLf & "c")
re.MultiLine = True
WScript.Echo re.Test("a" & vbLf & "b" & vbLf & "c"), re.MultiLine`, "False\nTrue True"},
		{"non-capturing and alternation", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "^(?:cat|dog)s?$"
WScript.Echo re.Test("cats"), re.Test("dog"), re.Test("bird")`, "True True False"},
		{"lazy quantifier", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "<.+?>"
WScript.Echo re.Execute("<a><b>")(0).Value`, "<a>"},
		{"pattern roundtrip", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "x+"
WScript.Echo re.Pattern`, "x+"},
		{"word boundary and classes", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "\bfoo\b" : re.Global = True
WScript.Echo re.Replace("foo foobar foo", "X")`, "X foobar X"},
		{"string escapes", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "a\tb"
WScript.Echo re.Test("a" & vbTab & "b")
re.Pattern = "\x41"
WScript.Echo re.Test("A")`, "True\nTrue"},
		{"typename", `
Set re = CreateObject("VBScript.RegExp")
WScript.Echo TypeName(re)`, "IRegExp2"},
		{"empty match replace global", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "x*" : re.Global = True
WScript.Echo re.Replace("abc", "-")`, "-a-b-c-"},
		{"unmatched group submatch is empty", `
Set re = CreateObject("VBScript.RegExp")
re.Pattern = "(a)|(b)"
Set m = re.Execute("b")(0)
WScript.Echo "[" & m.SubMatches(0) & "][" & m.SubMatches(1) & "]"`, "[][b]"},
	})
	runErrCases(t, []errCase{
		{"backreference unsupported", "Set re = CreateObject(\"VBScript.RegExp\")\nre.Pattern = \"(a)\\1\"\nx = re.Test(\"aa\")", 5017, 3, false},
		{"bad pattern", "Set re = CreateObject(\"VBScript.RegExp\")\nre.Pattern = \"(\"\nx = re.Test(\"a\")", 5017, 3, false},
	})
}

func TestRegExpLookaheadGap(t *testing.T) {
	t.Skip("known gap: lookahead (?=...) / (?!...) and back-references are unsupported (Go RE2 engine); they raise error 5017")
}
