# vbs

A VBScript interpreter in pure Go, used to run terminal-automation scripts
against a host object named `crt`. Entry points: `New`, `Run`, `Call`, `Eval`,
`SetGlobal`, `SetFunc`, `RegisterClass`, `SetArguments`, `SetOutput`, `Close`.
See `go doc` for the API.

## Supported

- Statements: `Dim/ReDim [Preserve]/Erase/Const/Public/Private`, `Set/Let`,
  `If/ElseIf/Else` (block and single-line), `Select Case`, `For/For Each`,
  `Do/While/Loop/Wend`, `Exit`, `With`, `Call`, `Option Explicit`,
  `On Error Resume Next / GoTo 0`, `Execute/ExecuteGlobal/Eval`, `Sub`,
  `Function` (ByRef/ByVal, recursion), `Class` (fields, methods,
  `Property Get/Let/Set`, `Default`, `Class_Initialize/Terminate`, `Private`).
- Operators with VBScript precedence, Null/Empty propagation, bitwise
  `And/Or/Xor/Eqv/Imp/Not`, banker's rounding for integer conversion.
- Literals: hex, octal, exponent, `#date#`.
- Built-ins: conversion, math, string, array, type-test, date/time and
  formatting families, `GetRef`, `Err` (Number/Description/Source/Clear/Raise),
  `vb*` constants.
- Objects: `Scripting.Dictionary`, `VBScript.RegExp`,
  `Scripting.FileSystemObject` (text streams, files, folders, paths),
  `WScript.Shell` (Run, Exec, ExpandEnvironmentStrings, Environment),
  `WScript` (Echo, Sleep, Quit, Arguments, StdIn/StdOut/StdErr).
- Host objects implement `Object`; Go errors become `Err` with number
  `HostErrorNumber` (1000), `*Error` values keep their number.
- Cancellation through the context is checked at every statement, loop
  iteration and `WScript.Sleep`, and cannot be trapped by `On Error`.

## Known gaps

- Only Integer/Long/Double/String/Boolean/Date/Null/Empty subtypes exist:
  `CSng`, `CCur` and `CByte` results report `TypeName` Double/Integer, and
  `Long` is reported only for values outside the Integer range.
- Regular expressions use Go's RE2: no lookahead or back-references (error
  5017).
- `Exit Do`/`Exit For` outside a matching loop is not a compile error.
- `WScript.Echo (obj).Member` parses `(obj)` as an argument list.
- `TextStream.WriteLine` uses the platform line ending (LF on non-Windows).
- `ByRef` passing of class public fields is by value.
- `WScript.Shell`: registry and `CreateShortcut` raise errors; `SendKeys`, `LogEvent` and `AppActivate` are no-ops.
