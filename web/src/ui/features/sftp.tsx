// SFTP browser (dual pane), file-transfer launchers and global transfer events.
import { ArrowUp, File as FileIcon, FolderPlus, Folder, Home, RefreshCw, X } from "lucide-react";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { rpc } from "../../rpc";
import { addSftpTab, dismissToast, fail, getApp, showDialog, Tab, toast } from "../../state/store";
import { confirm, promptText } from "../dialogs";
import { features, tabViews } from "../features";
import { FsEntry, formatSize, formatTime, pickPath, pickPaths } from "../filePicker";
import { Button, Check, Dialog, MenuItem, PopupMenu, Select, Text } from "../kit";
import "./sftp.css";

interface Job {
  id: string; session: number; direction: string; name: string; state: string; error?: string;
  size: number; done: number; files: number; filesDone: number; current?: string;
}
type Pane = "local" | "remote";
type SortKey = "name" | "size" | "mtime" | "perm";

const baseName = (p: string) => p.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || p;
const remoteJoin = (d: string, n: string) => (d.endsWith("/") ? d + n : d + "/" + n);
const remoteParent = (d: string) => { const t = d.replace(/\/+$/, ""); const i = t.lastIndexOf("/"); return i <= 0 ? "/" : t.slice(0, i); };

// ------------------------------------------------------------ dialogs

function TextEditor({ title, text, readOnly, close }: { title: string; text: string; readOnly: boolean; close: (v?: string) => void }) {
  const [v, setV] = useState(text);
  return (
    <Dialog title={title} width={760} height={520} onClose={() => close()}
      footer={<><span className="dim">{readOnly ? "Read only (too large or not text)" : ""}</span><span className="spacer" /><Button onClick={() => close()}>{readOnly ? "Close" : "Cancel"}</Button>{!readOnly && <Button primary onClick={() => close(v)}>Save</Button>}</>}>
      <textarea className="input sftp-edit" value={v} readOnly={readOnly} spellCheck={false} aria-label="File contents" onChange={(e) => setV(e.target.value)} />
    </Dialog>
  );
}

function PermDialog({ name, mode, close }: { name: string; mode: number; close: (v?: number) => void }) {
  const [m, setM] = useState(mode & 0o777);
  const [oct, setOct] = useState((mode & 0o777).toString(8).padStart(3, "0"));
  const set = (bit: number, on: boolean) => { const n = on ? m | bit : m & ~bit; setM(n); setOct(n.toString(8).padStart(3, "0")); };
  const rows: [string, number][] = [["Owner", 6], ["Group", 3], ["Others", 0]];
  const cols: [string, number][] = [["Read", 4], ["Write", 2], ["Execute", 1]];
  return (
    <Dialog title={"Permissions: " + name} width={400} onClose={() => close()} onSubmit={() => close(m)}
      footer={<><Button onClick={() => close()}>Cancel</Button><Button primary onClick={() => close(m)}>Apply</Button></>}>
      <div className="sftp-perm">
        <span />{cols.map(([c]) => <span key={c} className="dim">{c}</span>)}
        {rows.map(([r, sh]) => (
          <React.Fragment key={r}>
            <span>{r}</span>
            {cols.map(([c, b]) => <Check key={c} checked={!!(m & (b << sh))} onChange={(on) => set(b << sh, on)} label={<span className="sr" style={{ position: "absolute", width: 1, height: 1, overflow: "hidden" }}>{r} {c}</span>} />)}
          </React.Fragment>
        ))}
      </div>
      <div className="row"><span className="dim">Octal</span>
        <Text value={oct} width={80} mono onChange={(v) => { setOct(v); if (/^[0-7]{3,4}$/.test(v)) setM(parseInt(v, 8) & 0o777); }} /></div>
    </Dialog>
  );
}

// -------------------------------------------------------------- pane

interface PaneProps {
  kind: Pane; sid: number; start: string; tick: number; active: boolean;
  onDir: (d: string) => void;
  onTransfer: (paths: string[], from: Pane) => void;
  onDropFiles: (files: File[]) => void;
  onEdit?: (e: FsEntry) => void;
  onChanged: () => void;
}

function FilePane(p: PaneProps) {
  const remote = p.kind === "remote";
  const sep = remote ? "/" : getApp().info?.pathSep ?? "/";
  const [dir, setDir] = useState("");
  const [parent, setParent] = useState("");
  const [entries, setEntries] = useState<FsEntry[]>([]);
  const [error, setError] = useState("");
  const [pathText, setPathText] = useState("");
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [cur, setCur] = useState(0);
  const [anchor, setAnchor] = useState(0);
  const [sort, setSort] = useState<{ k: SortKey; asc: boolean }>({ k: "name", asc: true });
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [over, setOver] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const dirRef = useRef("");

  const load = useCallback(async (d: string, keepSel = false) => {
    try {
      const r = remote ? await rpc.call("sftp.list", { id: p.sid, path: d }) : await rpc.call("fs.list", { path: d });
      if (r.error) { setError(r.error); return; }
      setError(""); setEntries(r.entries ?? []); setDir(r.path); dirRef.current = r.path; setPathText(r.path);
      setParent(remote ? (r.path === "/" ? "" : remoteParent(r.path)) : r.parent && r.parent !== r.path ? r.parent : "");
      p.onDir(r.path);
      if (!keepSel) { setSel(new Set()); setCur(0); setAnchor(0); }
    } catch (e: any) { setError(String(e?.message ?? e)); }
  }, [p.sid, remote]);
  useEffect(() => { load(p.start || "~"); }, []);
  useEffect(() => { if (p.tick && dirRef.current) load(dirRef.current, true); }, [p.tick]);

  const rows = useMemo(() => {
    const f = sort.asc ? 1 : -1;
    const sorted = [...entries].sort((a, b) => {
      if (a.dir !== b.dir) return a.dir ? -1 : 1;
      let c = 0;
      if (sort.k === "name") c = a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" });
      else if (sort.k === "size") c = a.size - b.size;
      else if (sort.k === "mtime") c = a.mtime - b.mtime;
      else c = (a.mode ?? "").localeCompare(b.mode ?? "");
      return c * f || a.name.localeCompare(b.name);
    });
    const up: FsEntry[] = parent ? [{ name: "..", path: parent, size: 0, mode: "", perm: 0, mtime: 0, dir: true, link: false }] : [];
    return [...up, ...sorted];
  }, [entries, sort, parent]);
  const join = (n: string) => (remote ? remoteJoin(dir, n) : dir.endsWith(sep) ? dir + n : dir + sep + n);
  const selected = () => rows.filter((r) => r.name !== ".." && sel.has(r.path));
  const target = (): FsEntry[] => { const s = selected(); return s.length ? s : rows[cur] && rows[cur].name !== ".." ? [rows[cur]] : []; };

  const enter = (e: FsEntry) => (e.dir ? load(e.path) : p.onTransfer([e.path], p.kind));
  const click = (e: FsEntry, i: number, ev: React.MouseEvent) => {
    box.current?.focus();
    setCur(i);
    if (ev.shiftKey) {
      const [a, b] = [Math.min(anchor, i), Math.max(anchor, i)];
      setSel(new Set(rows.slice(a, b + 1).filter((r) => r.name !== "..").map((r) => r.path)));
    } else if (ev.ctrlKey || ev.metaKey) {
      const n = new Set(sel); n.has(e.path) ? n.delete(e.path) : n.add(e.path); setSel(n); setAnchor(i);
    } else { setSel(e.name === ".." ? new Set() : new Set([e.path])); setAnchor(i); }
  };

  const op = (m: string, a: any) => remote ? rpc.call("sftp." + m, { id: p.sid, ...a }) : rpc.call("fs." + m, a);
  const run = async (f: () => Promise<any>) => { try { await f(); } catch (e) { fail(e); } await load(dir, true); p.onChanged(); };
  const mkdir = async () => { const n = await promptText("New Folder", "Folder name:"); if (n?.trim()) run(() => op("mkdir", { path: join(n.trim()) })); };
  const rename = async () => {
    const t = target()[0]; if (!t) return;
    const n = await promptText("Rename", "New name for " + t.name + ":", t.name);
    if (n?.trim() && n !== t.name) run(() => op("rename", { path: t.path, to: join(n.trim()) }));
  };
  const del = async () => {
    const t = target(); if (!t.length) return;
    if (!(await confirm("Delete", t.length === 1 ? `Delete "${t[0].name}"?` : `Delete ${t.length} items?`, "Delete", true))) return;
    run(async () => { for (const e of t) await op("remove", { path: e.path }); });
  };
  const perms = async () => {
    const t = target()[0]; if (!t) return;
    const m = await showDialog<number>((close) => <PermDialog name={t.name} mode={t.perm} close={close} />);
    if (m !== undefined) run(() => rpc.call("sftp.chmod", { id: p.sid, path: t.path, mode: m }));
  };
  const copyPath = () => { const t = target()[0]; navigator.clipboard?.writeText(t ? t.path : dir).catch(() => {}); };

  const onKey = (e: React.KeyboardEvent) => {
    if ((e.target as HTMLElement).tagName === "INPUT") return;
    const mv = (n: number) => {
      n = Math.max(0, Math.min(rows.length - 1, n)); setCur(n);
      if (e.shiftKey) { const [a, b] = [Math.min(anchor, n), Math.max(anchor, n)]; setSel(new Set(rows.slice(a, b + 1).filter((r) => r.name !== "..").map((r) => r.path))); }
      else { setAnchor(n); setSel(rows[n] && rows[n].name !== ".." ? new Set([rows[n].path]) : new Set()); }
      box.current?.querySelector(`tr[data-i="${n}"]`)?.scrollIntoView({ block: "nearest" });
    };
    const k = e.key;
    if (k === "ArrowDown") mv(cur + 1);
    else if (k === "ArrowUp") mv(cur - 1);
    else if (k === "Home") mv(0);
    else if (k === "End") mv(rows.length - 1);
    else if (k === "Enter") rows[cur] && enter(rows[cur]);
    else if (k === "Backspace" && parent) load(parent);
    else if (k === "Delete") del();
    else if (k === "F2") rename();
    else if (k === "F5") load(dir, true);
    else if (k === "a" && (e.ctrlKey || e.metaKey)) setSel(new Set(rows.filter((r) => r.name !== "..").map((r) => r.path)));
    else if (k === "ContextMenu") { const r = box.current!.getBoundingClientRect(); setMenu({ x: r.left + 40, y: r.top + 60 }); }
    else return;
    e.preventDefault();
  };

  const menuItems = (): MenuItem[] => {
    const t = target(); const one = t.length === 1 ? t[0] : null;
    const tr: MenuItem = remote
      ? { label: "Download", disabled: !t.length, run: () => p.onTransfer(t.map((e) => e.path), "remote") }
      : { label: "Upload", disabled: !t.length, run: () => p.onTransfer(t.map((e) => e.path), "local") };
    const items: MenuItem[] = [tr];
    if (remote) items.push({ label: "View / Edit", disabled: !one || one.dir || one.size > 2 << 20, run: () => one && p.onEdit?.(one) });
    items.push({ sep: true }, { label: "Rename", keys: "F2", disabled: !one, run: rename }, { label: "Delete", keys: "Del", disabled: !t.length, run: del },
      { label: "New Folder…", run: mkdir });
    if (remote) items.push({ label: "Permissions…", disabled: !one, run: perms });
    items.push({ sep: true }, { label: "Copy Path", run: copyPath }, { label: "Refresh", keys: "F5", run: () => load(dir, true) });
    return items;
  };

  const drag = (e: React.DragEvent, r: FsEntry) => {
    const paths = sel.has(r.path) ? selected().map((x) => x.path) : [r.path];
    e.dataTransfer.setData("application/x-hawser-paths", JSON.stringify({ pane: p.kind, paths }));
    e.dataTransfer.effectAllowed = "copy";
  };
  const hasPayload = (e: React.DragEvent) => Array.from(e.dataTransfer.types).includes("application/x-hawser-paths") || (remote && Array.from(e.dataTransfer.types).includes("Files"));
  const drop = (e: React.DragEvent) => {
    e.preventDefault(); setOver(false);
    const raw = e.dataTransfer.getData("application/x-hawser-paths");
    if (raw) {
      const d = JSON.parse(raw);
      if (d.pane !== p.kind) p.onTransfer(d.paths, d.pane);
    } else if (remote && e.dataTransfer.files.length) p.onDropFiles(Array.from(e.dataTransfer.files));
  };
  const arrow = (k: SortKey) => (sort.k === k ? (sort.asc ? " ▲" : " ▼") : "");
  const th = (k: SortKey, label: string, w?: number | string) => (
    <th style={{ width: w }} onClick={() => setSort((s) => ({ k, asc: s.k === k ? !s.asc : true }))} aria-sort={sort.k === k ? (sort.asc ? "ascending" : "descending") : "none"}>{label}{arrow(k)}</th>
  );

  return (
    <div className={"sftp-pane" + (over ? " drop" : "")} data-pane={p.kind} ref={box} tabIndex={0} onKeyDown={onKey}
      onDragOver={(e) => { if (hasPayload(e)) { e.preventDefault(); setOver(true); } }}
      onDragLeave={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setOver(false); }} onDrop={drop}>
      <header>
        <span className="title">{remote ? "Remote" : "Local"}</span>
        <button className="tb-btn" title="Parent folder" aria-label="Parent folder" disabled={!parent} onClick={() => load(parent)}><ArrowUp size={15} /></button>
        <button className="tb-btn" title="Home" aria-label="Home" onClick={() => load("~")}><Home size={15} /></button>
        <button className="tb-btn" title="Refresh" aria-label="Refresh" onClick={() => load(dir, true)}><RefreshCw size={15} /></button>
        <button className="tb-btn" title="New folder" aria-label="New folder" onClick={mkdir}><FolderPlus size={15} /></button>
        <input className="input" aria-label={(remote ? "Remote" : "Local") + " path"} value={pathText} spellCheck={false}
          onChange={(e) => setPathText(e.target.value)} onFocus={(e) => e.currentTarget.select()}
          onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); load(pathText.trim() || "~"); box.current?.focus(); } else if (e.key === "Escape") { setPathText(dir); box.current?.focus(); } e.stopPropagation(); }} />
      </header>
      {error && <div className="sftp-err" role="alert">{error}</div>}
      <div className="sftp-scroll" onContextMenu={(e) => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY }); }}>
        <table className="sftp-table">
          <thead><tr>{th("name", "Name")}{th("size", "Size", 78)}{th("mtime", "Modified", 128)}{th("perm", "Permissions", 100)}</tr></thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={r.path + r.name} data-i={i} data-name={r.name} className={(sel.has(r.path) ? "sel" : "") + (i === cur && p.active ? " cur" : "")}
                draggable={r.name !== ".."} onDragStart={(e) => drag(e, r)}
                onClick={(e) => click(r, i, e)} onDoubleClick={() => enter(r)}
                onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); if (!sel.has(r.path)) { setSel(r.name === ".." ? new Set() : new Set([r.path])); setCur(i); setAnchor(i); } setMenu({ x: e.clientX, y: e.clientY }); }}>
                <td title={r.name}><div className="nm">{r.dir ? <Folder size={14} className="dir" /> : <FileIcon size={14} />}<span>{r.name}</span></div></td>
                <td className="num">{r.dir ? "" : formatSize(r.size)}</td>
                <td className="dim">{formatTime(r.mtime)}</td>
                <td className="dim mono">{r.name === ".." ? "" : r.mode}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {menu && <PopupMenu x={menu.x} y={menu.y} items={menuItems()} onClose={() => { setMenu(null); box.current?.focus(); }} />}
    </div>
  );
}

// ----------------------------------------------------------- the tab

function SftpView({ tab, active }: { tab: Tab; active: boolean }) {
  const sid = tab.sftp!.id;
  const [conflict, setConflict] = useState("overwrite");
  const [jobs, setJobs] = useState<Job[]>([]);
  const [closed, setClosed] = useState(false);
  const [ticks, setTicks] = useState({ local: 0, remote: 0 });
  const [focus, setFocus] = useState<Pane>("remote");
  const dirs = useRef({ local: "", remote: "" });
  const speed = useRef(new Map<string, { done: number; t: number; bps: number }>());
  const states = useRef(new Map<string, string>());
  const bump = (p: Pane) => setTicks((t) => ({ ...t, [p]: t[p] + 1 }));

  useEffect(() => {
    const onJob = (j: Job) => {
      if (j.session !== sid) return;
      const now = performance.now();
      const s = speed.current.get(j.id);
      if (j.state === "running") {
        if (s && now > s.t && j.done >= s.done) {
          const inst = ((j.done - s.done) * 1000) / (now - s.t);
          speed.current.set(j.id, { done: j.done, t: now, bps: s.bps ? s.bps * 0.6 + inst * 0.4 : inst });
        } else if (!s) speed.current.set(j.id, { done: j.done, t: now, bps: 0 });
      } else if (s) s.bps = 0;
      const prev = states.current.get(j.id);
      states.current.set(j.id, j.state);
      if (["done", "error", "cancelled"].includes(j.state) && prev !== j.state) bump(j.direction === "upload" ? "remote" : "local");
      setJobs((l) => (l.some((x) => x.id === j.id) ? l.map((x) => (x.id === j.id ? j : x)) : [...l, j]));
    };
    const off1 = rpc.on("sftp.job", onJob);
    const off2 = rpc.on("sftp.closed", (e: any) => { if (e?.id === sid) setClosed(true); });
    rpc.call("sftp.jobs", { id: sid }).then((l: Job[]) => { (l ?? []).forEach((j) => states.current.set(j.id, j.state)); setJobs((cur) => { const m = new Map((l ?? []).map((j) => [j.id, j])); cur.forEach((j) => m.set(j.id, j)); return [...m.values()]; }); }).catch(() => {});
    return () => { off1(); off2(); };
  }, [sid]);

  const transfer = (paths: string[], from: Pane) => {
    if (!paths.length) return;
    const direction = from === "local" ? "upload" : "download";
    const dest = from === "local" ? dirs.current.remote : dirs.current.local;
    rpc.call("sftp.transfer", { id: sid, direction, sources: paths, dest, conflict }).catch(fail);
  };
  const dropFiles = async (files: File[]) => {
    const paths: string[] = [];
    for (const f of files) {
      try {
        const r = await fetch("/api/upload?name=" + encodeURIComponent(f.name), { method: "POST", headers: { "X-Hawser-Token": rpc.token }, body: f });
        if (!r.ok) throw new Error(`Upload of ${f.name} failed (${r.status})`);
        paths.push((await r.json()).path);
      } catch (e) { fail(e); }
    }
    transfer(paths, "local");
  };
  const edit = async (e: FsEntry) => {
    try {
      const r = await rpc.call("sftp.read", { id: sid, path: e.path });
      // The editor works on LF text. Files that are not valid UTF-8 text would
      // be damaged by a round trip, so they open read-only; CRLF files get
      // their line endings back on save.
      const binary = r.text.includes("\ufffd") || r.text.includes("\u0000");
      const crlf = /\r\n/.test(r.text) && !/[^\r]\n/.test(r.text);
      const shown = crlf ? r.text.replace(/\r\n/g, "\n") : r.text;
      const text = await showDialog<string>((close) => <TextEditor title={e.name + (binary ? " (not UTF-8 text)" : "")} text={shown} readOnly={!!r.truncated || binary} close={close} />);
      if (text !== undefined && text !== shown) {
        await rpc.call("sftp.write", { id: sid, path: e.path, data: crlf ? text.replace(/\r?\n/g, "\r\n") : text });
        toast("Saved " + e.name);
        bump("remote");
      }
    } catch (err) { fail(err); }
  };
  const clear = () => rpc.call("sftp.clear", { id: sid }).then((l: Job[]) => setJobs(l ?? [])).catch(fail);
  const finished = jobs.some((j) => ["done", "error", "cancelled"].includes(j.state));

  return (
    <div className="sftp" data-sftp={sid}>
      <div className="sftp-bar">
        <span className="lbl">If file exists</span>
        <Select value={conflict as any} width={130} onChange={setConflict} options={[["overwrite", "Overwrite"], ["skip", "Skip"], ["resume", "Resume"]]} />
        <span className="spacer" style={{ flex: 1 }} />
      </div>
      {closed && <div className="sftp-closed" role="alert">The SFTP session is closed. Reopen it from the File menu.</div>}
      <div className="sftp-panes" onFocusCapture={(e) => { const p = (e.target as HTMLElement).closest<HTMLElement>("[data-pane]")?.dataset.pane as Pane; if (p) setFocus(p); }}>
        <FilePane kind="local" sid={sid} start="~" tick={ticks.local} active={active && focus === "local"} onDir={(d) => (dirs.current.local = d)}
          onTransfer={transfer} onDropFiles={() => {}} onChanged={() => {}} />
        <FilePane kind="remote" sid={sid} start={tab.sftp!.home} tick={ticks.remote} active={active && focus === "remote"} onDir={(d) => (dirs.current.remote = d)}
          onTransfer={transfer} onDropFiles={dropFiles} onEdit={edit} onChanged={() => {}} />
      </div>
      <div className="sftp-queue">
        <header><span>Transfers</span><span style={{ flex: 1 }} /><Button small disabled={!finished} onClick={clear}>Clear Finished</Button></header>
        <div className="sftp-scroll">
          {jobs.length === 0 && <div className="dim" style={{ padding: "8px 10px" }}>No transfers yet. Double-click a file or drag rows between the panes.</div>}
          {jobs.map((j) => {
            const pct = j.size > 0 ? Math.min(100, (j.done / j.size) * 100) : j.state === "done" ? 100 : 0;
            const bps = j.state === "running" ? speed.current.get(j.id)?.bps ?? 0 : 0;
            const live = j.state === "running" || j.state === "queued";
            return (
              <div key={j.id} className={"sftp-job " + j.state} data-job={j.name}>
                <span title={j.current || j.name}>{j.direction === "upload" ? "↑ " : "↓ "}{j.name}</span>
                <span className={"st-" + j.state}>{j.state}</span>
                <span>{j.files > 1 ? `${j.filesDone}/${j.files} files` : `${j.filesDone}/${j.files || 1} file`}</span>
                <div className="bar" role="progressbar" aria-valuenow={Math.round(pct)}><i style={{ width: pct + "%" }} /></div>
                <span title={j.error}>{j.error ? <span className="st-error">{j.error}</span> : `${formatSize(j.done)} / ${formatSize(j.size)}${bps ? " · " + formatSize(bps) + "/s" : ""}`}</span>
                {live ? <button className="tb-btn" style={{ width: 22, height: 22 }} title="Cancel" aria-label="Cancel transfer" onClick={() => rpc.call("sftp.cancel", { id: sid, job: j.id }).catch(fail)}><X size={14} /></button> : <span />}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

tabViews.sftp = SftpView;

features.sftpOpen = async (arg: { conn?: number; session?: string; overrides?: any; password?: string } = {}) => {
  try {
    const r = await rpc.call("sftp.open", arg);
    addSftpTab({ id: r.id, title: r.title, home: r.home, session: r.session });
  } catch (e) { fail(e); }
};

// ----------------------------------------------- serial-style transfers

features.xferStart = async (arg: { conn: number; protocol: "zmodem" | "ymodem" | "xmodem" | "kermit" | "ascii"; direction: "upload" | "download" }) => {
  const { conn: id, protocol, direction } = arg;
  try {
    if (direction === "upload") {
      const many = protocol === "zmodem" || protocol === "ymodem" || protocol === "kermit";
      const files = await pickPaths({ title: `${protocol.toUpperCase()} upload`, mode: many ? "openMany" : "open", ok: "Send" });
      if (!files?.length) return;
      await rpc.call("xfer.start", { id, protocol, direction, files });
    } else if (protocol === "xmodem") {
      const dest = await pickPath({ title: "XMODEM download: save as", mode: "save", ok: "Receive" });
      if (!dest) return;
      await rpc.call("xfer.start", { id, protocol, direction, dest });
    } else await rpc.call("xfer.start", { id, protocol, direction });
  } catch (e) { fail(e); }
};

features.logStart = async (arg: { conn: number }) => {
  try {
    const file = await pickPath({ title: "Log session to file", mode: "save", defaultName: "session.log", ok: "Start Logging" });
    if (!file) return;
    await rpc.call("conn.log.start", { id: arg.conn, file });
    toast("Logging to " + file);
  } catch (e) { fail(e); }
};

rpc.on("xfer.uploadRequest", async (ev: { id: number; protocol: string }) => {
  const files = await pickPaths({ title: `${ev.protocol.toUpperCase()}: files to send`, mode: "openMany", ok: "Send" });
  rpc.call("xfer.answerUpload", { id: ev.id, files: files ?? [] }).catch(fail);
});

const xferToasts = new Map<string, number>();
rpc.on("xfer.progress", (ev: any) => {
  const key = "xfer:" + ev.transfer;
  const proto = String(ev.protocol).toUpperCase();
  if (ev.kind === "done" || ev.kind === "error") {
    const old = xferToasts.get(key);
    if (old !== undefined) dismissToast(old);
    xferToasts.delete(key);
    if (ev.kind === "done") toast("Transfer complete" + (ev.dir ? ": " + ev.dir : ""), "info", { key });
    else toast(`${proto} transfer failed: ${ev.error || "error"}`, "error", { key });
    return;
  }
  const pct = ev.size > 0 ? Math.min(100, Math.round((ev.bytes / ev.size) * 100)) : undefined;
  const count = ev.count > 1 ? ` (${(ev.index ?? 0) + 1}/${ev.count})` : "";
  const text = `${proto} ${ev.direction}: ${ev.file ?? ""}${count} ${pct !== undefined ? pct + "% · " : ""}${formatSize(ev.bytes ?? 0)}${ev.size ? " / " + formatSize(ev.size) : ""}`;
  xferToasts.set(key, toast(text, "info", { key, sticky: true, progress: pct !== undefined ? pct / 100 : undefined }));
});
