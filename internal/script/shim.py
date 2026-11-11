# Hawser Python scripting shim: talks line-delimited JSON on the original
# stdin/stdout with the Go host. Python 3 only.
import io
import json
import os
import sys
import threading
import traceback

_rd = sys.stdin
_wr = sys.stdout
_lock = threading.Lock()
_seq = [0]
_methods = {}


class _Quit(BaseException):
    pass


def _send(m):
    with _lock:
        _wr.write(json.dumps(m, default=str) + "\n")
        _wr.flush()


def _wrap(v):
    if isinstance(v, dict) and "$obj" in v:
        return _Obj(v["$obj"], v["type"])
    if isinstance(v, list):
        return [_wrap(x) for x in v]
    return v


def _call(op, h, name, args=None, value=None):
    _seq[0] += 1
    m = {"id": _seq[0], "op": op, "obj": h, "name": name}
    if args is not None:
        m["args"] = [a._ref() if isinstance(a, _Obj) else a for a in args]
    if op == "set":
        m["value"] = value
    _send(m)
    line = _rd.readline()
    if not line:
        raise _Quit()
    r = json.loads(line)
    if r.get("ok"):
        return _wrap(r.get("value"))
    if r.get("quit"):
        raise _Quit()
    raise RuntimeError(r.get("error", "script host error"))


class _Obj(object):
    def __init__(self, h, t):
        object.__setattr__(self, "_h", h)
        object.__setattr__(self, "_t", t)

    def _ref(self):
        return {"$obj": self._h}

    def __getattr__(self, name):
        if name.startswith("__"):
            raise AttributeError(name)
        if name.lower() in _methods.get(self._t, ()):
            def caller(*args):
                return _call("call", self._h, name, list(args))
            caller.__name__ = name
            return caller
        return _call("get", self._h, name)

    def __setattr__(self, name, value):
        _call("set", self._h, name, value=value)

    def __getitem__(self, i):
        return _call("call", self._h, "", [i])

    def __call__(self, *args):
        return _call("call", self._h, "", list(args))

    def __bool__(self):
        return True

    def __len__(self):
        if self._t != "Arguments":
            raise TypeError("object of type '%s' has no len()" % self._t)
        return _call("get", self._h, "Count")

    def __iter__(self):
        if self._t != "Arguments":
            raise TypeError("'%s' object is not iterable" % self._t)
        return iter([self[i] for i in range(len(self))])

    def __repr__(self):
        return "<crt.%s>" % self._t


class _Out(io.TextIOBase):
    encoding = "utf-8"

    def write(self, s):
        if s:
            _send({"op": "output", "text": s})
        return len(s)

    def flush(self):
        pass

    def isatty(self):
        return False

    def writable(self):
        return True


def _main():
    init = json.loads(_rd.readline())
    for t, names in init["methods"].items():
        _methods[t] = set(n.lower() for n in names)
    crt = _wrap(init["crt"])
    sys.stdout = sys.stderr = _Out()
    sys.stdin = io.StringIO("")
    path = init.get("path") or "<script>"
    sys.argv = [path] + list(init.get("args") or [])
    if init.get("path"):
        sys.path.insert(0, os.path.dirname(os.path.abspath(path)))
    g = {"__name__": "__main__", "__file__": path, "crt": crt}
    err = ""
    try:
        code = compile(init["source"], path, "exec")
        exec(code, g)
    except (_Quit, SystemExit):
        pass
    except BaseException:
        et, ev, tb = sys.exc_info()
        if tb is not None and tb.tb_next is not None:
            tb = tb.tb_next
        err = "".join(traceback.format_exception(et, ev, tb))
    _send({"op": "done", "error": err})


_main()
