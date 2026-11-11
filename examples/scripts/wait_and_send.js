// $language = "JScript"
// Wait for a shell prompt, run a command and show the last screen lines.
crt.Screen.Synchronous = true;
crt.Screen.Send("date\r");
if (crt.Screen.WaitForString("20", 10)) {
  crt.Sleep(300);
  var rows = crt.Screen.Rows;
  var text = crt.Screen.Get(Math.max(1, crt.Screen.CurrentRow - 4), 1, crt.Screen.CurrentRow, crt.Screen.Columns);
  crt.Dialog.MessageBox(text, "Last lines (" + rows + " rows)");
} else {
  crt.Dialog.MessageBox("Timed out waiting for output.", "Script");
}
