// Scripting: engine-driven dialogs and screen access for crt scripts, running
// and stopping scripts, and the macro recorder.
import { useState } from "react";
import { rpc } from "../../rpc";
import {
  activeTab, controllers, dismissToast, fail, focusTab, getApp, patchTab, showDialog, tabByConn, toast,
} from "../../state/store";
import { alertBox, promptText } from "../dialogs";
import { features } from "../features";
import { pickPath } from "../filePicker";
import { Button, Dialog, Select } from "../kit";
import { copyText } from "./tools";

const base = (p: string) => p.split(/[\\/]/).pop() || p;
const dirOf = (p: string) => {
  const i = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\"));
  return i > 0 ? p.slice(0, i) : p.slice(0, i + 1);
};

function store(key: string, v?: string): string {
  try {
    if (v !== undefined) localStorage.setItem(key, v);
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

// ------------------------------------------------------------- message box

const BUTTON_SETS: [number, string][][] = [
  [[1, "OK"]],
  [[1, "OK"], [2, "Cancel"]],
  [[3, "Abort"], [4, "Retry"], [5, "Ignore"]],
  [[6, "Yes"], [7, "No"], [2, "Cancel"]],
  [[6, "Yes"], [7, "No"]],
  [[4, "Retry"], [2, "Cancel"]],
];

function messageBox(msg: string, title: string, buttons: number, signal: AbortSignal): Promise<number> {
  const set = BUTTON_SETS[buttons & 0xf] ?? BUTTON_SETS[0];
  const dismiss = (set.find(([v]) => v === 2) ?? set.find(([v]) => v === 7) ?? set[set.length - 1])[0];
  return new Promise((resolve) => {
    showDialog<number>((close) => {
      signal.addEventListener("abort", () => close(dismiss));
      return (
        <Dialog title={title || "Hawser"} onClose={() => close(dismiss)} onSubmit={() => close(set[0][0])}
          footer={<>{set.map(([v, label], i) => <Button key={v} primary={i === 0} onClick={() => close(v)}>{label}</Button>)}</>}>
          <div style={{ whiteSpace: "pre-wrap", userSelect: "text" }}>{msg}</div>
        </Dialog>
      );
    }).then((v) => resolve(v ?? dismiss));
  });
}

function suffixes(filter: string): string[] {
  // "*.txt;*.log", "Text files|*.txt", ".py"
  const part = filter.includes("|") ? filter.split("|").pop()! : filter;
  return part.split(/[;,\s]+/).map((s) => s.replace(/^\*/, "").trim()).filter((s) => s.startsWith(".")).map((s) => s.toLowerCase());
}

rpc.handle("script.dialog", async (d, signal) => {
  switch (d.kind) {
    case "messageBox":
      return { value: await messageBox(String(d.message ?? ""), String(d.title ?? ""), Number(d.buttons ?? 0), signal) };
    case "prompt": {
      const v = await promptText(String(d.title || "Hawser"), String(d.message ?? ""), String(d.default ?? ""), { secret: !!d.password });
      return { ok: v !== undefined, value: v ?? "" };
    }
    case "fileOpen": {
      const p = await pickPath({
        mode: "open", title: d.title || "Open", ok: d.button || "Open", defaultName: d.defaultName || undefined,
        filter: d.filter ? suffixes(String(d.filter)) : undefined, startDir: store("hawser.scriptDir") || undefined,
      });
      return { path: p ?? "" };
    }
  }
  throw new Error("unknown dialog kind " + d.kind);
});

// ------------------------------------------------------------- screen access

const lastWrite = new Map<number, number>();
const patched = new WeakSet<object>();

/** Waits until the terminal has been quiet for 30 ms (at most 500 ms). */
async function settle(conn: number): Promise<void> {
  const tab = tabByConn(conn);
  const c = tab && controllers.get(tab.id);
  if (!c) return;
  if (!patched.has(c.term)) {
    patched.add(c.term);
    const term: any = c.term;
    const write = term.write.bind(term);
    term.write = (p: Uint8Array) => {
      lastWrite.set(conn, performance.now());
      return write(p);
    };
  }
  const start = performance.now();
  for (;;) {
    const now = performance.now();
    if (now - (lastWrite.get(conn) ?? 0) >= 30 || now - start >= 500) return;
    await new Promise((r) => setTimeout(r, 10));
  }
}

function controllerOf(conn: number) {
  const tab = tabByConn(conn);
  const c = tab && controllers.get(tab.id);
  if (!c) throw new Error("the session has no terminal window");
  return c;
}

rpc.handle("script.screen", async (d) => {
  await settle(d.conn);
  const c = controllerOf(d.conn);
  if (d.op === "text") return { text: c.screenGet(d.r1, d.c1, d.r2, d.c2) };
  if (d.op === "info") {
    return { rows: c.term.rows, cols: c.term.cols, curRow: c.term.buf.y + 1, curCol: c.term.buf.x + 1, selection: c.selectionText() };
  }
  throw new Error("unknown screen operation " + d.op);
});

// ------------------------------------------------------------------ window

function tabIds(): number[] {
  const s = getApp();
  const out: number[] = [];
  for (const g of s.groups) for (const id of g.tabs) {
    const t = s.tabs[id];
    if (t && t.kind === "terminal") out.push(t.connId);
  }
  return out;
}

rpc.handle("script.ui", async (d) => {
  switch (d.op) {
    case "tabs":
      return { tabs: tabIds(), active: activeTab()?.connId ?? 0 };
    case "activate": {
      const t = tabByConn(d.conn);
      if (t) focusTab(t.id);
      return null;
    }
    case "setCaption": {
      if (!d.conn) document.title = d.caption;
      else {
        const t = tabByConn(d.conn);
        if (t) patchTab(t.id, { customTitle: d.caption });
      }
      return null;
    }
    case "getCaption": {
      if (!d.conn) return { caption: document.title };
      const t = tabByConn(d.conn);
      return { caption: t?.customTitle ?? t?.title ?? "" };
    }
    case "clipboardGet": {
      try { return { text: await navigator.clipboard.readText() }; } catch { return { text: clipFallback }; }
    }
    case "clipboardSet": {
      clipFallback = String(d.text ?? "");
      try { await navigator.clipboard.writeText(clipFallback); } catch { /* denied: keep the in-page copy */ }
      return null;
    }
    case "status":
      toast(String(d.text ?? ""), "info", { key: "script-status" });
      return null;
    case "openSftp":
      features.sftpOpen?.({ conn: d.conn });
      return null;
  }
  throw new Error("unknown operation " + d.op);
});
let clipFallback = "";

// ------------------------------------------------------------------ running

interface RunInfo { id: string; conn: number; path: string; lang: string; state: string; error?: string; output?: string }
const toasts = new Map<string, { toast: number; file: string }>();

function ResultDialog(p: { file: string; info: RunInfo; close: () => void }) {
  return (
    <Dialog title={`Script error: ${p.file}`} width={640} onClose={p.close} onSubmit={p.close} footer={<Button primary onClick={p.close}>OK</Button>}>
      <pre style={{ whiteSpace: "pre-wrap", userSelect: "text", margin: 0, maxHeight: 280, overflow: "auto", color: "var(--danger)" }}>{p.info.error}</pre>
      {p.info.output && <>
        <div className="dim" style={{ margin: "10px 0 4px" }}>Output</div>
        <pre style={{ whiteSpace: "pre-wrap", userSelect: "text", margin: 0, maxHeight: 200, overflow: "auto" }}>{p.info.output}</pre>
      </>}
    </Dialog>
  );
}

rpc.on("script.state", (info: RunInfo) => {
  const file = info.path ? base(info.path) : "script";
  const prev = toasts.get(info.id);
  if (info.state === "running") {
    if (!prev) {
      const id = toast(`Running ${file} … (click to stop)`, "info", { sticky: true, key: "script-" + info.id });
      toasts.set(info.id, { toast: id, file });
    }
    return;
  }
  if (prev) {
    dismissToast(prev.toast);
    toasts.delete(info.id);
  }
  if (info.state === "error") showDialog<void>((close) => <ResultDialog file={file} info={info} close={() => close()} />);
  else if (info.state === "stopped") toast(`Script ${file} stopped`);
  else toast(`Script ${file} finished`);
});

// a click on the sticky "Running …" toast stops that script
document.addEventListener("click", (e) => {
  const el = (e.target as Element | null)?.closest?.(".toast");
  const text = el?.textContent ?? "";
  if (!text.startsWith("Running ") || !text.includes("(click to stop)")) return;
  for (const [id, r] of toasts) if (text.startsWith(`Running ${r.file} `)) rpc.call("script.stop", { id }).catch(fail);
}, true);

features.scriptRun = async (a: { conn?: number; path?: string; args?: string[] | string } = {}) => {
  try {
    const t = a.conn ? tabByConn(a.conn) : activeTab();
    if (!t || t.kind !== "terminal") return toast("Open a terminal tab to run a script in.");
    let path = a.path;
    if (!path) {
      path = await pickPath({ mode: "open", title: "Run Script", ok: "Run", filter: [".py", ".vbs", ".js", ".txt"], startDir: store("hawser.scriptDir") || undefined });
      if (!path) return;
      store("hawser.scriptDir", dirOf(path));
    }
    const args = typeof a.args === "string" ? a.args.split(/\s+/).filter(Boolean) : (a.args ?? []);
    await rpc.call("script.run", { conn: t.connId, path, args });
  } catch (e) { fail(e); }
};

features.scriptStop = async (a: { conn?: number } = {}) => {
  const conn = a.conn ?? activeTab()?.connId;
  if (!conn) return;
  rpc.call("script.stop", { conn }).catch(fail);
};

// ------------------------------------------------------------------ recorder

const LANGS = [["python", "Python"], ["javascript", "JavaScript"], ["vbscript", "VBScript"]] as const;
const EXT: Record<string, string> = { python: ".py", javascript: ".js", vbscript: ".vbs" };

function ScriptView(p: { lang: string; script: string; close: () => void }) {
  const save = async () => {
    const path = await pickPath({ mode: "save", title: "Save Script", defaultName: "recorded" + EXT[p.lang], ok: "Save", filter: Object.values(EXT), startDir: store("hawser.scriptDir") || undefined });
    if (!path) return;
    try {
      await rpc.call("fs.writeText", { path, data: p.script });
      store("hawser.scriptDir", dirOf(path));
      toast("Saved " + base(path));
    } catch (e) { fail(e); }
  };
  return (
    <Dialog title="Recorded Script" width={640} onClose={p.close}
      footer={<><Button onClick={() => copyText(p.script)}>Copy</Button><Button onClick={save}>Save As…</Button><span className="spacer" /><Button primary onClick={p.close}>Close</Button></>}>
      <pre style={{ whiteSpace: "pre", userSelect: "text", margin: 0, maxHeight: 360, overflow: "auto", fontFamily: "var(--mono, monospace)" }}>{p.script}</pre>
    </Dialog>
  );
}

function LangPick(p: { close: (v?: string) => void }) {
  const [lang, setLang] = useState<string>("python");
  return (
    <Dialog title="Stop Recording" width={360} onClose={() => p.close()} onSubmit={() => p.close(lang)}
      footer={<><Button onClick={() => p.close()}>Cancel</Button><Button primary onClick={() => p.close(lang)}>Generate</Button></>}>
      <div style={{ marginBottom: 8 }}>Script language</div>
      <Select value={lang} onChange={setLang} options={LANGS as any} />
    </Dialog>
  );
}

features.recordStop = async (a: { conn?: number } = {}) => {
  const conn = a.conn ?? activeTab()?.connId;
  if (!conn) return;
  const lang = await showDialog<string>((close) => <LangPick close={close} />);
  if (!lang) return;
  try {
    const r = await rpc.call("conn.record.stop", { id: conn, lang });
    if (!r?.script) return void alertBox("Recorder", "Nothing was recorded.");
    await showDialog<void>((close) => <ScriptView lang={lang} script={r.script} close={() => close()} />);
  } catch (e) { fail(e); }
};
