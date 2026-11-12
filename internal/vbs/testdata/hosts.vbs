Option Explicit
' Read a host list (hostsFile is provided by the host) and probe each entry.

Dim fso, ts, line, hosts, up, down, i, n
Set fso = CreateObject("Scripting.FileSystemObject")
Set hosts = CreateObject("Scripting.Dictionary")

Set ts = fso.OpenTextFile(hostsFile, 1)
Do While Not ts.AtEndOfStream
    line = Trim(ts.ReadLine)
    If Len(line) > 0 And Left(line, 1) <> "#" Then
        Dim parts
        parts = Split(line, ",")
        If UBound(parts) >= 1 Then
            hosts.Add Trim(parts(0)), Trim(parts(1))
        Else
            hosts.Add line, "default"
        End If
    End If
Loop
ts.Close

up = 0 : down = 0
For Each i In hosts
    crt.Screen.Send "ping " & i & vbCr
    n = crt.Screen.WaitForStrings("is alive", "unreachable", 5)
    If n = 1 Then
        up = up + 1
        WScript.Echo i & " (" & hosts(i) & "): up"
    ElseIf n = 2 Then
        down = down + 1
        WScript.Echo i & " (" & hosts(i) & "): DOWN"
    Else
        down = down + 1
        WScript.Echo i & " (" & hosts(i) & "): timeout"
    End If
Next
WScript.Echo "summary: " & up & " up, " & down & " down of " & hosts.Count
