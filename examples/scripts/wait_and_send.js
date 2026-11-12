// $language = "JavaScript"
// Send a command, wait for its output and show the last lines of the screen.
// Run it with Script > Run... on a connected tab with a shell.
crt.Screen.Synchronous = true;   // never miss output that arrives between calls

// The arithmetic keeps the marker out of the echo of the command line.
crt.Screen.Send("echo started-$((40+2))\r");
if (crt.Screen.WaitForString("started-42", 10)) {
  crt.Sleep(300);                // let the prompt arrive
  var last = crt.Screen.CurrentRow;
  var text = crt.Screen.Get(Math.max(1, last - 4), 1, last, crt.Screen.Columns);
  crt.Dialog.MessageBox(text, "Last lines (" + crt.Screen.Rows + " rows)");
} else {
  crt.Dialog.MessageBox("Timed out waiting for output.", "Script");
}
