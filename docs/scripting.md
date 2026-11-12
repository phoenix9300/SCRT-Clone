# Scripting

Hawser runs automation scripts written in **Python** (`.py`), **JavaScript**
(`.js`) or **VBScript** (`.vbs`). All three see the same global object, `crt`.
The language is chosen by the file extension, or by a header line such as
`# $language = "Python"`.

Run a script with **Script → Run…**, from a button-bar button, as a session's
logon script (Session Options → Logon Actions), or from the command line:

```bash
hawser --script login.py --arg router1 "Lab/core-sw1"
```

VBScript runs on every platform through Hawser's own interpreter; a `Sub Main`
is called automatically. Python scripts run in your Python 3 interpreter
(Global Options → Python path) and may import any module. JavaScript runs in a
built-in ECMAScript engine.

```python
# $language = "Python"
crt.Screen.Synchronous = True
crt.Screen.Send("show version\r")
if crt.Screen.WaitForString("#", 10):
    text = crt.Screen.Get(1, 1, crt.Screen.Rows, crt.Screen.Columns)
    crt.Dialog.MessageBox(text)
```

Member names are not case-sensitive. Time-outs are in seconds (fractions are
allowed); omitted or `0` waits forever.

## crt

| Member | Description |
| --- | --- |
| `Screen`, `Session`, `Dialog`, `Window`, `Arguments`, `Clipboard`, `FileTransfer`, `CommandWindow` | objects of the tab the script was started in |
| `ScriptFullName`, `Version` | path of the running script, application version |
| `Sleep(ms)` | pause |
| `Quit()` | end the script |
| `GetTab(n)`, `GetTabCount()`, `GetActiveTab()`, `GetScriptTab()` | tab objects (1-based index) |
| `OpenSessionConfiguration(path)` | a stored session's settings (`""` = default session) |
| `GetLastError()`, `GetLastErrorMessage()`, `ClearLastError()` | status of the last failed call |

## Screen

| Member | Description |
| --- | --- |
| `Send(text)`, `SendKeys(text)` | send text to the host (`\r` = Enter) |
| `SendSpecial(name)`, `SendBreak()` | `"TN_BREAK"` sends a break |
| `WaitForString(text [, timeout [, ignoreCase]])` | `True` when the text arrives |
| `WaitForStrings(list… [, timeout])` | 1-based index of the first match, `0` on time-out; also in `MatchIndex` |
| `ReadString(delimiter… [, timeout])` | text received before the delimiter |
| `WaitForCursor([timeout])` | `True` when the cursor moves |
| `Get(row1, col1, row2, col2)` | screen text (rows concatenated) |
| `Get2(row1, col1, row2, col2)` | screen text with CR LF between rows |
| `CurrentRow`, `CurrentColumn`, `Rows`, `Columns`, `Selection` | screen state |
| `Synchronous` | when `True`, waits never miss output that arrived since the last wait |
| `IgnoreCase`, `IgnoreEscape`, `MatchIndex` | matching options and result |

## Session

| Member | Description |
| --- | --- |
| `Connected`, `Path`, `RemoteAddress`, `RemotePort`, `LocalAddress` | state |
| `Connect([arg])` | reconnect, or connect the tab elsewhere: `/S "Folder/Name"`, `/SSH2 /L user /P 22 /PASSWORD pw host`, `/TELNET host [port]` |
| `ConnectInTab(arg)` | same, in a new tab; returns the tab |
| `Disconnect()` | |
| `Log(start [, append [, raw]])`, `Logging`, `LogFileName` | session logging |
| `SetStatusText(text)` | show a message |

## Dialog

| Member | Description |
| --- | --- |
| `MessageBox(text [, title [, buttons]])` | returns `1` OK, `2` Cancel, `3` Abort, `4` Retry, `5` Ignore, `6` Yes, `7` No |
| `Prompt(text [, title [, default [, isPassword]]])` | returns the text, `""` when cancelled |
| `FileOpenDialog([title [, button [, defaultName [, filter]]]])` | returns a path or `""` |

## Tab, Window, Arguments, Clipboard

| Member | Description |
| --- | --- |
| `Tab.Caption`, `Tab.Index`, `Tab.Screen`, `Tab.Session` | |
| `Tab.Activate()`, `Tab.Clone()`, `Tab.Close()`, `Tab.ConnectSftp()` | |
| `Window.Caption` | window title |
| `Arguments.Count`, `Arguments.GetArg(i)` | script arguments (0-based); also `crt.Arguments(0)` / `crt.Arguments[0]` |
| `Clipboard.Text` | read or set the clipboard |

## FileTransfer, CommandWindow, SessionConfiguration

| Member | Description |
| --- | --- |
| `FileTransfer.ZmodemUploadAddFile(path)`, `ClearZmodemUploadList()` | files sent when the host next starts `rz` |
| `FileTransfer.SendXmodem(path)`, `ReceiveXmodem(path)`, `SendYmodem(path)`, `ReceiveYmodem()`, `SendAscii(path)` | |
| `CommandWindow.Text`, `Send()`, `SendToAllSessions` | send a command to this or every session |
| `SessionConfiguration.GetOption(name)`, `SetOption(name, value)`, `Save([path])`, `ConnectInTab()` | names are JSON paths such as `terminal.scrollback`, or the aliases `Hostname`, `Username`, `Port`, `Protocol Name`, `Emulation`, `Description` |

## Recording

**Script → Start Recording Script** captures what you type together with the
prompt that preceded it; **Stop Recording** turns it into a script in the
language of your choice.

Examples are in [`examples/scripts`](../examples/scripts).
