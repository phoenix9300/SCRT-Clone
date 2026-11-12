# $language = "VBScript"
# $interface = "1.0"
' Log in to a device over an already open session and run one command.
Option Explicit

Const PROMPT = "admin@router:~$ "
Dim result

Sub Main
    Dim user, pass, ok
    user = crt.Arguments(0)
    pass = crt.Arguments(1)

    crt.Screen.Synchronous = True
    crt.Screen.Send "ssh " & user & "@10.0.0.1" & vbCr

    ok = crt.Screen.WaitForStrings("Password:", "Permission denied", 10)
    Select Case ok
        Case 1
            crt.Screen.Send pass & vbCr
        Case Else
            crt.Dialog.MessageBox "no password prompt"
            Exit Sub
    End Select

    If Not crt.Screen.WaitForString(PROMPT, 10) Then
        WScript.Echo "login failed"
        Exit Sub
    End If
    WScript.Echo "logged in as " & user

    crt.Screen.Send "uptime" & vbCr
    result = Trim(crt.Screen.ReadString(vbCr))
    WScript.Echo "uptime: " & result
End Sub

Call Main
