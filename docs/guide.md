# User guide

## Starting

```bash
hawser
```

opens the application window. Targets on the command line are opened as tabs:

```bash
hawser "Lab/core-sw1" ssh://admin@10.0.0.1:2222 telnet://pbx.example.net serial:/dev/ttyUSB0:115200 local
```

| Option | Meaning |
| --- | --- |
| `--browser app\|default\|none` | application window (default), your default browser, or no window |
| `--listen ADDR` | address of the engine (default `127.0.0.1:0`) |
| `--allow-remote`, `--tls-cert`, `--tls-key` | serve the UI to another machine, over HTTPS |
| `--config-dir DIR` | use another configuration directory (also `HAWSER_CONFIG`) |
| `--script FILE`, `--arg VALUE` | run a script once the window is ready |
| `--new-instance` | do not hand the targets to an already running Hawser |

`HAWSER_BROWSER` selects the browser used for the application window.

## Sessions

A session is a saved connection. Create one with **File → New Session…**, or
use **Quick Connect** (`Alt+Q`) and tick *Save session*. The session manager
on the left shows sessions in folders; double-click to connect, right-click
for *Properties*, *Duplicate*, *Rename*, *Delete*, *Connect SFTP* and *Open at
Startup*. Drag sessions between folders.

Sessions inherit every setting from the **Default Session** (Options →
Default Session…) and store only what differs, so changing a default changes
all sessions that did not override it.

Each session is a small JSON file under `sessions/` in the configuration
directory; passwords are never in those files, they are in the encrypted
vault. Import existing inventories with **Tools → Import Sessions…** (OpenSSH
config, PuTTY, CSV, other terminals' exports).

**Jump hosts:** set *Firewall* on a session's Connection page to another SSH2
session and the connection is tunnelled through it.

**Shared credentials:** Tools → Credentials… stores a user name and password
once; pick it on the Connection page of any number of sessions.

## Working in the terminal

| Keys | Action |
| --- | --- |
| `Ctrl+Shift+C` / `Ctrl+Insert` | copy |
| `Ctrl+Shift+V` / `Shift+Insert` | paste |
| `Ctrl+Shift+F` | find in scrollback |
| `Shift+PageUp` / `Shift+PageDown` | scroll |
| `Ctrl+Tab`, `Ctrl+Shift+Tab`, `Alt+1…9` | switch tabs |
| `Ctrl+Shift+T` | new local shell |
| `Ctrl+Shift+D` | clone session (same SSH connection) |
| `Ctrl+Shift+R` | reconnect (or press Enter in a disconnected tab) |
| `Ctrl+=`, `Ctrl+-`, `Ctrl+0` | zoom |
| `F11` | full screen |

Double-click selects a word, triple-click a line, `Alt`+drag a block,
`Shift`+click extends. Hold `Ctrl` and click a URL to open it. All shortcuts
can be changed in Options → Keyboard Shortcuts…, and each session can map keys
to strings (Session Options → Mapped Keys).

**Tab groups:** Window → Move to New Tab Group splits the window; drag tabs
between groups.

**Command window** (View menu) sends what you type to the active session, to
the sessions of a tab group, or to all sessions. The **button bar** holds
buttons that send strings, run scripts or invoke menu commands.

**Keyword highlighting:** define sets of patterns in Options → Keyword
Highlighting… and select a set on a session's Appearance page.

## Logging

File → Log Session writes clean text (control sequences removed); *Raw Log*
writes every byte. Session Options → Log File sets the file name, where these
are substituted: `%H` host, `%S` session, `%P` port, `%U` user, `%Y %M %D`
date, `%h %m %s %t` time. The same codes work in the custom lines written at
connect, disconnect and at the start of each line (for timestamps).

## File transfer

- **SFTP tab** (File → Connect SFTP Tab): local files on the left, remote on
  the right; double-click or drag to transfer, including folders.
- **ZMODEM:** run `sz file` on the host to download, or `rz` to be asked which
  files to upload. Downloads go to the session's download folder.
- **XMODEM / YMODEM / Kermit / ASCII:** start the transfer on the host, then
  choose the matching entry in the Transfer menu.
- **TFTP server:** Tools → TFTP Server….

## Keys and security

- Tools → Create Public Key… generates RSA, ECDSA or Ed25519 keys; Convert
  Private Key… converts between OpenSSH, PEM and PuTTY formats.
- Identity files are tried only when the server accepts their public half,
  so you are asked for a passphrase only when it matters. Manage Agent Keys…
  holds decrypted keys for the current run.
- New host keys are shown for confirmation and stored in `known_hosts`; a
  changed key raises a warning. The policy (ask / accept new / strict) is per
  session.
- Tools → Credential Vault… sets a master passphrase for stored secrets.

See also [scripting.md](scripting.md).
