' $language = "VBScript"
' Connect to each host of a list in a new tab and run a command on it.
Sub Main
  hosts = Array("web1.example.com", "web2.example.com", "db1.example.com")
  user = crt.Dialog.Prompt("User name for all hosts:", "Host list", "admin")
  If user = "" Then Exit Sub

  For Each h In hosts
    On Error Resume Next
    Set tab = crt.Session.ConnectInTab("/SSH2 /L " & user & " " & h)
    If Err.Number <> 0 Or crt.GetLastError <> 0 Then
      WScript.Echo h & ": " & crt.GetLastErrorMessage
      crt.ClearLastError
      Err.Clear
    Else
      tab.Screen.Synchronous = True
      tab.Screen.Send "hostname; uptime" & vbCr
      tab.Screen.WaitForString "load average", 10
      WScript.Echo h & ": ok"
    End If
    On Error GoTo 0
  Next
End Sub
