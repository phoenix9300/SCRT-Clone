#!/usr/bin/env python3
"""Record the raw output of a terminal program driven by scripted input.

usage: record.py OUT.bin COLS ROWS TERM 'shell command' [key-script]

The key script is a list of steps separated by '|': each step is either
"wait:<seconds>" or literal text with python-style escapes (\\r, \\x1b ...).
"""
import os, pty, sys, time, select, struct, fcntl, termios, codecs

out, cols, rows, term, cmd = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], sys.argv[5]
script = sys.argv[6] if len(sys.argv) > 6 else ""

pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = term
    os.environ["LANG"] = "C.UTF-8"
    os.environ["LC_ALL"] = "C.UTF-8"
    os.environ.pop("COLORTERM", None)
    os.execvp("/bin/sh", ["/bin/sh", "-c", cmd])
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

data = bytearray()

def pump(seconds):
    end = time.time() + seconds
    while True:
        left = end - time.time()
        if left <= 0:
            return True
        r, _, _ = select.select([fd], [], [], left)
        if fd in r:
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                return False
            if not chunk:
                return False
            data.extend(chunk)

alive = pump(0.8)
for step in [s for s in script.split("|") if s]:
    if not alive:
        break
    if step.startswith("wait:"):
        alive = pump(float(step[5:]))
    else:
        os.write(fd, codecs.decode(step, "unicode_escape").encode("latin1"))
        alive = pump(0.4)
if alive:
    pump(0.6)
try:
    os.kill(pid, 9)
except OSError:
    pass
open(out, "wb").write(bytes(data))
print(f"{out}: {len(data)} bytes")
