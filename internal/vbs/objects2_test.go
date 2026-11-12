package vbs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// vbsPath returns dir as a script-safe path (forward slashes work on all
// platforms).
func vbsPath(dir string) string { return filepath.ToSlash(dir) }

func runIn(t *testing.T, dir, src string) string {
	t.Helper()
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	in.SetGlobal("dir", vbsPath(dir))
	if err := in.Run(context.Background(), src, "fso.vbs"); err != nil {
		t.Fatalf("error: %v\noutput:\n%s", err, buf.String())
	}
	return strings.TrimRight(buf.String(), "\n")
}

func TestFSOTextStreams(t *testing.T) {
	dir := t.TempDir()
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	in.SetGlobal("dir", vbsPath(dir))
	in.SetGlobal("eol", lineEnding) // WriteLine uses the platform line ending
	err := in.Run(context.Background(), `
Set fso = CreateObject("Scripting.FileSystemObject")
p = dir & "/a.txt"
Set f = fso.CreateTextFile(p, True)
f.WriteLine "line1"
f.WriteLine "line2"
f.Write "x" & "y"
f.WriteBlankLines 2
f.Write "end"
f.Close
WScript.Echo fso.FileExists(p)

Set f = fso.OpenTextFile(p, 1)
WScript.Echo f.AtEndOfStream, f.Line
WScript.Echo f.ReadLine, f.Line
Do While Not f.AtEndOfStream
  n = n + 1
  s = s & "[" & f.ReadLine & "]"
Loop
f.Close
WScript.Echo n, s

Set f = fso.OpenTextFile(p, 1)
all = f.ReadAll
f.Close
WScript.Echo Len(all), UBound(Split(all, eol))

Set f = fso.OpenTextFile(p, 1)
WScript.Echo f.Read(3), f.Column
f.SkipLine
WScript.Echo f.ReadLine
f.Close

Set f = fso.OpenTextFile(p, 8)
f.WriteLine
f.WriteLine "appended"
f.Close
Set f = fso.OpenTextFile(p, 1)
all = f.ReadAll : f.Close
WScript.Echo Right(all, Len(eol) + 8 + Len(eol)) = eol & "appended" & eol

Set f = fso.OpenTextFile(p, 2)
f.Write "over"
f.Close
WScript.Echo fso.OpenTextFile(p).ReadAll, fso.GetFile(p).Size

Set f = fso.OpenTextFile(dir & "/new.txt", 8, True)
f.Write "made"
f.Close
WScript.Echo fso.OpenTextFile(dir & "/new.txt").ReadAll
`, "fso.vbs")
	if err != nil {
		t.Fatal(err)
	}
	want := "True\nFalse 1\nline1 2\n4 [line2][xy][][end]\n"
	if !strings.HasPrefix(buf.String(), want) {
		t.Fatalf("unexpected output:\n%s", buf.String())
	}
	tail := strings.TrimPrefix(buf.String(), want)
	eol := len(lineEnding)
	allLen := 5 + eol + 5 + eol + 2 + 2*eol + 3
	if want := strconv.Itoa(allLen) + " 4\nlin 4\nline2\nTrue\nover 4\nmade\n"; tail != want {
		t.Errorf("tail = %q, want %q", tail, want)
	}
}

func TestFSOLineEndings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unix.txt"), []byte("a\nb\r\nc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runIn(t, dir, `
Set fso = CreateObject("Scripting.FileSystemObject")
Set f = fso.OpenTextFile(dir & "/unix.txt")
Do Until f.AtEndOfStream
  s = s & "<" & f.ReadLine & ">"
Loop
f.Close
Set f = fso.CreateTextFile(dir & "/out.txt")
f.WriteLine "x"
f.Close
WScript.Echo s, fso.GetFile(dir & "/out.txt").Size`)
	if want := "<a><b><c> " + strconv.Itoa(1+len(lineEnding)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFSOFilesAndFolders(t *testing.T) {
	dir := t.TempDir()
	got := runIn(t, dir, `
Set fso = CreateObject("Scripting.FileSystemObject")
fso.CreateFolder dir & "/sub"
WScript.Echo fso.FolderExists(dir & "/sub"), fso.FolderExists(dir & "/nope"), fso.FileExists(dir & "/sub")
Set f = fso.CreateTextFile(dir & "/sub/one.txt") : f.Write "12345" : f.Close
Set f = fso.CreateTextFile(dir & "/sub/two.log") : f.Close
fso.CopyFile dir & "/sub/one.txt", dir & "/sub/copy.txt"
fso.MoveFile dir & "/sub/two.log", dir & "/sub/moved.log"
WScript.Echo fso.FileExists(dir & "/sub/copy.txt"), fso.FileExists(dir & "/sub/two.log"), fso.FileExists(dir & "/sub/moved.log")

Set fl = fso.GetFile(dir & "/sub/one.txt")
WScript.Echo fl.Name, fl.Size, fl.Type <> "", fso.GetExtensionName(fl.Path), fl.ParentFolder.Name

Set folder = fso.GetFolder(dir & "/sub")
WScript.Echo folder.Name, folder.Files.Count, folder.SubFolders.Count
For Each x In folder.Files
  names = names & x.Name & ";"
Next
WScript.Echo names

fso.CreateFolder dir & "/sub/inner"
WScript.Echo folder.SubFolders.Count
For Each d In folder.SubFolders
  WScript.Echo d.Name
Next

fso.CopyFolder dir & "/sub", dir & "/sub2"
WScript.Echo fso.FileExists(dir & "/sub2/one.txt"), fso.FolderExists(dir & "/sub2/inner")
fso.MoveFolder dir & "/sub2", dir & "/sub3"
WScript.Echo fso.FolderExists(dir & "/sub2"), fso.FolderExists(dir & "/sub3")

fso.GetFile(dir & "/sub/copy.txt").Delete
fso.DeleteFile dir & "/sub/moved.log"
WScript.Echo fso.FileExists(dir & "/sub/copy.txt"), fso.FileExists(dir & "/sub/moved.log")
fso.GetFile(dir & "/sub/one.txt").Copy dir & "/sub/dup.txt"
fso.GetFile(dir & "/sub/dup.txt").Move dir & "/sub/dup2.txt"
WScript.Echo fso.FileExists(dir & "/sub/dup2.txt")
fso.DeleteFolder dir & "/sub3"
WScript.Echo fso.FolderExists(dir & "/sub3")
fso.DeleteFile dir & "/sub/*.txt"
WScript.Echo folder.Files.Count
`)
	want := "True False False\nTrue False True\none.txt 5 True txt sub\nsub 3 0\nmoved.log;one.txt;copy.txt;\n"
	_ = want
	lines := strings.Split(got, "\n")
	exp := []string{
		"True False False",
		"True False True",
		"one.txt 5 True txt sub",
		"sub 3 0",
	}
	for i, w := range exp {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	// Enumeration order is not guaranteed by VBScript; compare as a set.
	for _, n := range []string{"one.txt;", "copy.txt;", "moved.log;"} {
		if !strings.Contains(lines[4], n) {
			t.Errorf("file listing %q lacks %s", lines[4], n)
		}
	}
	rest := strings.Join(lines[5:], "|")
	if rest != "1|inner|True True|False True|False False|True|False|0" {
		t.Errorf("rest = %q", rest)
	}
}

func TestFSOPathHelpers(t *testing.T) {
	runCases(t, []tcase{
		{"names", `
Set fso = CreateObject("Scripting.FileSystemObject")
p = "/data/logs/app.tar.gz"
WScript.Echo fso.GetFileName(p), fso.GetBaseName(p), fso.GetExtensionName(p), fso.GetParentFolderName(p)`, "app.tar.gz app.tar gz /data/logs"},
		{"no extension", `
Set fso = CreateObject("Scripting.FileSystemObject")
WScript.Echo "[" & fso.GetExtensionName("/a/b") & "]", fso.GetBaseName("/a/b")`, "[] b"},
		{"build path", `
Set fso = CreateObject("Scripting.FileSystemObject")
WScript.Echo Replace(fso.BuildPath("/a/b", "c.txt"), "\", "/"), Replace(fso.BuildPath("/a/b/", "c.txt"), "\", "/")`, "/a/b/c.txt /a/b/c.txt"},
		{"temp name", `
Set fso = CreateObject("Scripting.FileSystemObject")
n = fso.GetTempName
WScript.Echo Len(n) > 0, TypeName(n)`, "True String"},
		{"absolute path", `
Set fso = CreateObject("Scripting.FileSystemObject")
p = fso.GetAbsolutePathName("x.txt")
WScript.Echo Len(p) > Len("x.txt"), fso.GetFileName(p)`, "True x.txt"},
		{"typename", `Set fso = CreateObject("Scripting.FileSystemObject") : WScript.Echo TypeName(fso)`, "FileSystemObject"},
	})
}

func TestFSOErrors(t *testing.T) {
	dir := t.TempDir()
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	in.SetGlobal("dir", vbsPath(dir))
	err := in.Run(context.Background(), `
Set fso = CreateObject("Scripting.FileSystemObject")
On Error Resume Next
fso.OpenTextFile dir & "/missing.txt"
WScript.Echo Err.Number : Err.Clear
fso.GetFile dir & "/missing.txt"
WScript.Echo Err.Number : Err.Clear
fso.GetFolder dir & "/missing"
WScript.Echo Err.Number : Err.Clear
fso.DeleteFile dir & "/missing.txt"
WScript.Echo Err.Number : Err.Clear
fso.CreateFolder dir
WScript.Echo Err.Number : Err.Clear
fso.CreateTextFile dir & "/x.txt", False
fso.CreateTextFile dir & "/x.txt", False
WScript.Echo Err.Number : Err.Clear
Set f = fso.OpenTextFile(dir & "/x.txt", 1)
f.Close
f.ReadLine
WScript.Echo Err.Number : Err.Clear
Set f = fso.OpenTextFile(dir & "/x.txt", 1)
f.ReadLine
WScript.Echo Err.Number
`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(buf.String())
	want := []string{"53", "53", "76", "53", "58", "58", "54", "62"}
	if len(got) < len(want) {
		t.Fatalf("got %v", got)
	}
	names := []string{"open missing", "getfile missing", "getfolder missing", "deletefile missing", "createfolder exists", "create no overwrite", "closed stream", "read past end"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("%s: err = %s, want %s", names[i], got[i], w)
		}
	}
}

func TestWScriptShell(t *testing.T) {
	t.Setenv("VBS_TEST_VAR", "hello")
	runCases(t, []tcase{
		{"expand env", `
Set sh = CreateObject("WScript.Shell")
WScript.Echo sh.ExpandEnvironmentStrings("[%VBS_TEST_VAR%]"), sh.ExpandEnvironmentStrings("no vars")`, "[hello] no vars"},
		{"expand unknown stays", `
Set sh = CreateObject("WScript.Shell")
WScript.Echo sh.ExpandEnvironmentStrings("%VBS_SURELY_UNSET_VAR%")`, "%VBS_SURELY_UNSET_VAR%"},
		{"environment", `
Set sh = CreateObject("WScript.Shell")
Set env = sh.Environment("PROCESS")
WScript.Echo env("VBS_TEST_VAR")`, "hello"},
		{"typename", `Set sh = CreateObject("WScript.Shell") : WScript.Echo TypeName(sh)`, "IWshShell3"},
		{"current directory", `
Set sh = CreateObject("WScript.Shell")
WScript.Echo Len(sh.CurrentDirectory) > 0`, "True"},
	})
	if runtime.GOOS == "windows" {
		t.Skip("shell command tests need a POSIX shell")
	}
	out, err := runOut(t, `
Set sh = CreateObject("WScript.Shell")
rc = sh.Run("true", 0, True)
WScript.Echo rc
rc = sh.Run("exit 3", 0, True)
WScript.Echo rc
Set ex = sh.Exec("echo hello")
Do While ex.Status = 0 : WScript.Sleep 10 : Loop
WScript.Echo ex.StdOut.ReadAll, ex.ExitCode`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "0\n3\nhello") || !strings.HasSuffix(out, "0") {
		t.Errorf("shell output = %q", out)
	}
}

func TestWScriptObject(t *testing.T) {
	runCases(t, []tcase{
		{"echo forms", `
WScript.Echo "a", "b", 3
WScript.Echo
WScript.Echo "x" & vbTab & "y"
WScript.Echo True, Empty = ""`, "a b 3\n\nx\ty\nTrue True"},
		{"echo array element", `WScript.Echo Array(1, 2)(1)`, "2"},
		{"stdout", `
WScript.StdOut.WriteLine "one"
WScript.StdOut.Write "two"
WScript.StdOut.WriteLine
WScript.StdOut.WriteBlankLines 1
WScript.StdOut.Write "three"`, "one\ntwo\n\nthree"},
		{"sleep short", `
t = Timer
WScript.Sleep 20
WScript.Echo Timer - t >= 0.015`, "True"},
		{"script name", `WScript.Echo WScript.ScriptName`, "test.vbs"},
		{"name", `WScript.Echo WScript.Name, WScript.Version <> ""`, "Windows Script Host True"},
		{"createobject through wscript", `
Set d = WScript.CreateObject("Scripting.Dictionary")
WScript.Echo TypeName(d)`, "Dictionary"},
		{"no arguments", `WScript.Echo WScript.Arguments.Count`, "0"},
		{"interactive", `WScript.Echo WScript.Interactive`, "True"},
	})
}

func TestWScriptArguments(t *testing.T) {
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	in.SetArguments("alpha", "beta", "/flag", "k:v")
	err := in.Run(context.Background(), `
Set a = WScript.Arguments
WScript.Echo a.Count, a(0), a.Item(1), a.Length
For i = 0 To a.Count - 1
  s = s & a(i) & ","
Next
WScript.Echo s
For Each x In a
  t = t & x
Next
WScript.Echo t
WScript.Echo a.Unnamed.Count, a.Named.Count`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	want := "4 alpha beta 4\nalpha,beta,/flag,k:v,\nalphabeta/flagk:v\n"
	if !strings.HasPrefix(buf.String(), want) {
		t.Errorf("got %q", buf.String())
	}
}

func TestWScriptQuit(t *testing.T) {
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	err := in.Run(context.Background(), "WScript.Echo \"before\"\nWScript.Quit 7\nWScript.Echo \"after\"", "t.vbs")
	if !errors.Is(err, ErrQuit) {
		t.Fatalf("err = %v, want ErrQuit", err)
	}
	if in.ExitCode() != 7 || buf.String() != "before\n" {
		t.Errorf("exit=%d out=%q", in.ExitCode(), buf.String())
	}

	// Quit cannot be trapped, also from inside procedures.
	in = New()
	err = in.Run(context.Background(), "Sub S : WScript.Quit 2 : End Sub\nOn Error Resume Next\nS\nWScript.Echo \"no\"", "t.vbs")
	if !errors.Is(err, ErrQuit) || in.ExitCode() != 2 {
		t.Errorf("err=%v exit=%d", err, in.ExitCode())
	}

	// Without an argument the exit code is 0.
	in = New()
	if err := in.Run(context.Background(), "WScript.Quit", "t.vbs"); !errors.Is(err, ErrQuit) || in.ExitCode() != 0 {
		t.Errorf("err=%v exit=%d", err, in.ExitCode())
	}
}

func TestSleepIsSkippableByCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	in := New()
	start := time.Now()
	err := in.Run(ctx, "WScript.Sleep 60000", "t.vbs")
	if !errors.Is(err, context.Canceled) || time.Since(start) > 200*time.Millisecond {
		t.Errorf("err=%v after %v", err, time.Since(start))
	}
}
