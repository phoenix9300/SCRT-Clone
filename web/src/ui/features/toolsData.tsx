// Tools: TFTP server panel, session import and export.
import React, { useEffect, useRef, useState } from "react";
import { rpc } from "../../rpc";
import { fail, getApp, reloadTree, showDialog, toast } from "../../state/store";
import { features } from "../features";
import { formatSize, pickPath } from "../filePicker";
import { Button, Check, Dialog, Field, Num, Select, Text } from "../kit";

const open = (render: (close: () => void) => React.ReactNode) => { showDialog<void>((close) => <>{render(() => close())}</>); };
const pad = (n: number) => String(n).padStart(2, "0");
const clock = (ms: number) => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`; };

// ------------------------------------------------------------ TFTP

function Tftp({ close }: { close: () => void }) {
  const [st, setSt] = useState<any>(null);
  const [root, setRoot] = useState("");
  const [port, setPort] = useState(69);
  const [rd, setRd] = useState(true);
  const [wr, setWr] = useState(false);
  const [ow, setOw] = useState(false);
  const [events, setEvents] = useState<any[]>([]);
  const [err, setErr] = useState("");
  const log = useRef<HTMLPreElement>(null);
  useEffect(() => {
    rpc.call("tftp.status").then((s) => {
      setSt(s); setEvents(s.events ?? []);
      if (s.running) { setRoot(s.root); setRd(s.allowRead); setWr(s.allowWrite); setOw(s.allowOverwrite); setPort(Number(String(s.addr).split(":").pop()) || 69); }
    }).catch(fail);
    const o1 = rpc.on("tftp.event", (e) => setEvents((l) => [...l, e].slice(-200)));
    const o2 = rpc.on("tftp.status", (s) => setSt((p: any) => ({ ...p, ...s })));
    return () => { o1(); o2(); };
  }, []);
  useEffect(() => { log.current?.scrollTo(0, log.current.scrollHeight); }, [events]);
  const running = !!st?.running;
  const start = async () => {
    setErr("");
    if (!root.trim()) return setErr("Choose the directory to serve.");
    if (!rd && !wr) return setErr("Allow reading, writing or both.");
    try { setSt(await rpc.call("tftp.start", { root: root.trim(), port, allowRead: rd, allowWrite: wr, allowOverwrite: wr && ow })); } catch (e) { setErr(String((e as Error).message)); }
  };
  const stop = () => rpc.call("tftp.stop").then(setSt).catch(fail);
  return (
    <Dialog title="TFTP Server" width={640} onClose={close} onSubmit={running ? undefined : start}
      footer={<><span className={"tl-status " + (running ? "on" : "")} role="status">{running ? `Listening on ${st.addr}` : "Stopped"}</span><span className="spacer" />
        {running ? <Button danger onClick={stop}>Stop</Button> : <Button primary onClick={start}>Start</Button>}<Button onClick={close}>Close</Button></>}>
      <Field label="Root directory"><Text value={root} onChange={setRoot} mono disabled={running} /><Button disabled={running} onClick={async () => { const p = await pickPath({ mode: "folder", title: "TFTP root directory", startDir: root || undefined }); if (p) setRoot(p); }}>Browse…</Button></Field>
      <Field label="Port" hint="Ports below 1024 (the default 69) may need administrator privileges; 6969 works without."><Num value={port} onChange={setPort} min={1} max={65535} disabled={running} /></Field>
      <div style={{ marginLeft: 162 }}>
        <Check checked={rd} onChange={setRd} label="Allow clients to read (download) files" disabled={running} />
        <Check checked={wr} onChange={setWr} label="Allow clients to write (upload) files" disabled={running} />
        <Check checked={ow} onChange={setOw} label="Allow overwriting existing files" disabled={running || !wr} />
      </div>
      {err && <div className="error-text" role="alert">{err}</div>}
      <div className="tl-sec">Activity</div>
      <pre className="tl-log" ref={log} aria-label="TFTP activity log" aria-live="polite" data-testid="tftp-log">
        {events.length ? events.map((e, i) => (
          <div key={i} className={e.error ? "err" : ""}>{clock(e.time)} {e.kind.padEnd(5)} {e.remote} {e.file}{e.bytes ? ` (${formatSize(e.bytes)})` : ""}{e.error ? " - " + e.error : ""}</div>
        )) : <span className="dim">No activity yet.</span>}
      </pre>
    </Dialog>
  );
}

// ------------------------------------------------------------ import

const SOURCES: { id: string; label: string; hint: string; def: string; kind: "file" | "either" }[] = [
  { id: "openssh", label: "OpenSSH config", hint: "A ssh_config file; each non-wildcard Host becomes a session.", def: "~/.ssh/config", kind: "file" },
  { id: "putty", label: "PuTTY sessions", hint: "The ~/.putty/sessions folder, or a Windows registry export (.reg).", def: "~/.putty/sessions", kind: "either" },
  { id: "csv", label: "CSV file", hint: "Header row with name, folder, protocol, hostname, port, username, description, emulation.", def: "", kind: "file" },
  { id: "hawser", label: "Hawser export (JSON)", hint: "A file written by Export Sessions.", def: "", kind: "file" },
  { id: "crt", label: "Other terminal's sessions", hint: "A folder of .ini session files or a single XML export from another terminal client.", def: "", kind: "either" },
];

function ImportSessions({ close }: { close: () => void }) {
  const [kind, setKind] = useState("openssh");
  const [path, setPath] = useState(SOURCES[0].def);
  const [items, setItems] = useState<any[] | null>(null);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const [folder, setFolder] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [err, setErr] = useState("");
  const [summary, setSummary] = useState<any>(null);
  const src = SOURCES.find((s) => s.id === kind)!;
  const pick = (k: string) => { const s = SOURCES.find((x) => x.id === k)!; setKind(k); setPath(s.def); setItems(null); setErr(""); };
  const browse = async (mode: "open" | "folder") => {
    const p = await pickPath({ mode, title: "Choose " + src.label, startDir: path && !path.startsWith("~") ? path.replace(/[\\/][^\\/]*$/, "") : undefined });
    if (p) { setPath(p); setItems(null); }
  };
  const preview = async () => {
    setErr(""); setItems(null);
    if (!path.trim()) return setErr("Choose a file or folder first.");
    try {
      const r = await rpc.call("import.preview", { kind, path: path.trim() });
      setItems(r.items); setWarnings(r.warnings ?? []); setChecked(new Set(r.items.map((_: any, i: number) => i)));
      if (!r.items.length) setErr("No sessions were found there.");
    } catch (e) { setErr(String((e as Error).message)); }
  };
  const apply = async () => {
    const sel = items!.filter((_, i) => checked.has(i));
    if (!sel.length) return setErr("Select at least one session.");
    try {
      const r = await rpc.call("import.apply", { items: sel, folder: folder.trim(), overwrite });
      await reloadTree();
      setSummary(r);
    } catch (e) { setErr(String((e as Error).message)); }
  };
  if (summary) {
    return (
      <Dialog title="Import finished" width={460} onClose={close} footer={<Button primary onClick={close}>Close</Button>}>
        <p style={{ marginTop: 0 }}>Created {summary.created}, skipped {summary.skipped} existing{summary.errors?.length ? `, ${summary.errors.length} failed` : ""}.</p>
        {summary.errors?.map((e: string, i: number) => <div key={i} className="error-text">{e}</div>)}
      </Dialog>
    );
  }
  const all = items && items.length > 0 && checked.size === items.length;
  return (
    <Dialog title="Import Sessions" width={760} onClose={close} onSubmit={items ? undefined : preview}
      footer={<><Button onClick={close}>Cancel</Button>{items ? <Button primary disabled={!checked.size} onClick={apply}>Import {checked.size}</Button> : <Button primary onClick={preview}>Preview</Button>}</>}>
      <Field label="Source" hint={src.hint}><Select value={kind} onChange={pick} options={SOURCES.map((s) => [s.id, s.label] as const)} /></Field>
      <Field label={src.kind === "either" ? "File or folder" : "File"}>
        <Text value={path} onChange={(v) => { setPath(v); setItems(null); }} mono />
        {src.kind === "either" ? <><Button onClick={() => browse("folder")}>Folder…</Button><Button onClick={() => browse("open")}>File…</Button></> : <Button onClick={() => browse("open")}>Browse…</Button>}
      </Field>
      {items && items.length > 0 && <>
        <div className="tl-scroll" style={{ marginTop: 6 }}>
          <table className="tl-table" aria-label="Sessions found">
            <thead><tr><th style={{ width: 30 }}><input type="checkbox" aria-label="Select all" checked={!!all} onChange={(e) => setChecked(e.target.checked ? new Set(items.map((_, i) => i)) : new Set())} /></th>
              <th style={{ width: "30%" }}>Path</th><th style={{ width: 80 }}>Protocol</th><th>Host</th><th className="num" style={{ width: 54 }}>Port</th><th style={{ width: "16%" }}>User</th></tr></thead>
            <tbody>
              {items.map((it, i) => (
                <tr key={i}>
                  <td><input type="checkbox" aria-label={"Import " + it.path} checked={checked.has(i)} onChange={(e) => { const n = new Set(checked); e.target.checked ? n.add(i) : n.delete(i); setChecked(n); }} /></td>
                  <td title={it.path}>{it.path}</td><td>{it.overrides.protocol}</td><td title={it.overrides.host}>{it.overrides.host ?? it.overrides.serial?.port ?? ""}</td><td className="num">{it.overrides.port ?? ""}</td><td>{it.overrides.username ?? ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {warnings.map((w, i) => <div key={i} className="tl-warn">{w}</div>)}
        <div style={{ height: 8 }} />
        <Field label="Target folder" hint="Optional; sessions are created inside this folder."><Text value={folder} onChange={setFolder} placeholder="(top level)" /></Field>
        <div style={{ marginLeft: 162 }}><Check checked={overwrite} onChange={setOverwrite} label="Overwrite existing sessions with the same name" /></div>
      </>}
      {err && <div className="error-text" role="alert">{err}</div>}
    </Dialog>
  );
}

export function registerDataTools(): void {
  features.tftp = () => open((c) => <Tftp close={c} />);
  features.importSessions = () => open((c) => <ImportSessions close={c} />);
  features.exportSessions = async () => {
    const file = await pickPath({ mode: "save", title: "Export sessions", defaultName: "hawser-sessions.json", startDir: getApp().info?.homeDir });
    if (!file) return;
    try {
      const r = await rpc.call("export.sessions", { file });
      toast(`Exported ${r.count} session${r.count === 1 ? "" : "s"} to ${r.file}`);
    } catch (e) { fail(e); }
  };
}
