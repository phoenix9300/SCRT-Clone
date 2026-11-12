' $language = "VBScript"
' Open one tab per target and run a command in each.
'
' Every script argument is a connect string, for example
'     /S "Folder/Name"                     a stored session
'     /SSH2 /L admin web1.example.com      an SSH2 host
' With no arguments an example list of SSH2 hosts is used (the user name is
' asked for). Targets that cannot be reached are reported and skipped.
Sub Main
  If crt.Arguments.Count > 0 Then
    For i = 0 To crt.Arguments.Count - 1
      RunOn crt.Arguments.GetArg(i)
    Next
  Else
    user = crt.Dialog.Prompt("User name for all hosts:", "Host list", "admin")
    If user = "" Then Exit Sub
    For Each h In Array("web1.example.com", "web2.example.com", "db1.example.com")
      RunOn "/SSH2 /L " & user & " " & h
    Next
  End If
End Sub

Sub RunOn(target)
  ' host errors raise like any VBScript error; the details are also in
  ' crt.GetLastErrorMessage until crt.ClearLastError
  On Error Resume Next
  Set tab = crt.Session.ConnectInTab(target)
  If Err.Number <> 0 Then
    WScript.Echo target & ": " & Err.Description
    crt.ClearLastError
    Err.Clear
    Exit Sub
  End If
  On Error GoTo 0

  tab.Screen.Synchronous = True
  ' the arithmetic keeps the marker out of the echo of the command line
  tab.Screen.Send "hostname; echo done-$((20+22))" & vbCr
  If tab.Screen.WaitForString("done-42", 15) Then
    WScript.Echo target & ": ok"
  Else
    WScript.Echo target & ": no answer"
  End If
End Sub
