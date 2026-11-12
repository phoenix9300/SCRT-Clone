Option Explicit
' Parse "show interfaces" output with a RegExp wrapped in a helper class.

Class InterfaceParser
    Private re
    Private items

    Private Sub Class_Initialize
        Set re = New RegExp
        re.Pattern = "^(\w+)\s+(UP|DOWN)\s+(\d+\.\d+\.\d+\.\d+)/(\d+)$"
        re.MultiLine = True
        re.Global = True
        Set items = CreateObject("Scripting.Dictionary")
    End Sub

    Public Sub Parse(text)
        Dim m
        items.RemoveAll
        For Each m In re.Execute(text)
            items.Add m.SubMatches(0), m.SubMatches(1) & " " & m.SubMatches(2) & "/" & m.SubMatches(3)
        Next
    End Sub

    Public Property Get Count
        Count = items.Count
    End Property

    Public Default Property Get Item(name)
        If items.Exists(name) Then Item = items(name) Else Item = "?"
    End Property

    Public Function Names
        Names = items.Keys
    End Function
End Class

Dim output, p, n
crt.Screen.Send "show interfaces" & vbCr
output = crt.Screen.ReadString("#")
Set p = New InterfaceParser
p.Parse Replace(output, vbCr, "")
WScript.Echo "found " & p.Count & " interfaces"
For Each n In p.Names
    WScript.Echo n & " -> " & p(n)
Next
WScript.Echo "lo9 -> " & p("lo9")
