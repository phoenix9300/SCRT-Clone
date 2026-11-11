// File chooser backed by the engine's view of the local file system (the
// browser cannot see real paths, the engine can).
import { ArrowUp, File as FileIcon, Folder, Home } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { rpc } from "../rpc";
import { getApp, showDialog } from "../state/store";
import { Button, Dialog, Text } from "./kit";

export interface FsEntry { name: string; path: string; size: number; mode: string; perm: number; mtime: number; dir: boolean; link: boolean; target?: string }

export interface PickOptions {
  title?: string;
  mode: "open" | "openMany" | "save" | "folder";
  startDir?: string;
  defaultName?: string;
  /** file name suffixes to show, e.g. [".py", ".vbs"]; empty = all */
  filter?: string[];
  ok?: string;
}

export function formatSize(n: number): string {
  if (n < 1024) return n + " B";
  const u = ["KB", "MB", "GB", "TB"];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) (v /= 1024), i++;
  return v.toFixed(v < 10 ? 1 : 0) + " " + u[i];
}

export function formatTime(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

let lastDir = "";

function Picker({ o, close }: { o: PickOptions; close: (v?: string[]) => void }) {
  const sep = getApp().info?.pathSep ?? "/";
  const [dir, setDir] = useState(o.startDir || lastDir || "~");
  const [entries, setEntries] = useState<FsEntry[]>([]);
  const [parent, setParent] = useState("");
  const [error, setError] = useState("");
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [name, setName] = useState(o.defaultName ?? "");
  const [pathText, setPathText] = useState("");
  const [all, setAll] = useState(!o.filter?.length);
  const seq = useRef(0);
  const typed = useRef(false); // the user edited the path box while a listing was in flight
  const load = (d: string) => {
    const mine = ++seq.current; // ignore answers that arrive after a newer request
    typed.current = false;
    return rpc.call("fs.list", { path: d }).then((r) => {
      if (mine !== seq.current) return;
      setEntries(r.entries ?? []); setError(r.error ?? ""); setParent(r.parent ?? "");
      if (!r.error) { setDir(r.path); if (!typed.current) setPathText(r.path); lastDir = r.path; setSel(new Set()); }
    }).catch((e) => mine === seq.current && setError(String(e.message)));
  };
  useEffect(() => { load(dir); }, []);
  const shown = entries.filter((e) => e.dir || (o.mode !== "folder" && (all || o.filter!.some((f) => e.name.toLowerCase().endsWith(f)))));
  const join = (n: string) => (dir.endsWith(sep) ? dir + n : dir + sep + n);
  const accept = () => {
    if (o.mode === "folder") return close([sel.size === 1 ? [...sel][0] : dir]);
    if (o.mode === "save") return name.trim() ? close([/[\\/]/.test(name) ? name.trim() : join(name.trim())]) : undefined;
    const files = [...sel].filter((p) => !entries.find((e) => e.path === p)?.dir);
    if (files.length) close(files);
  };
  const click = (e: FsEntry, ev: React.MouseEvent) => {
    if (o.mode === "save" && !e.dir) setName(e.name);
    if (o.mode === "openMany" && (ev.ctrlKey || ev.metaKey)) {
      const n = new Set(sel);
      n.has(e.path) ? n.delete(e.path) : n.add(e.path);
      setSel(n);
    } else setSel(new Set([e.path]));
  };
  return (
    <Dialog title={o.title ?? (o.mode === "save" ? "Save As" : o.mode === "folder" ? "Select Folder" : "Open")} width={640} height={520} flush onClose={() => close()}
      footer={<>
        {o.filter?.length ? <label className="check" style={{ margin: 0 }}><input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />All files</label> : null}
        <span className="spacer" /><Button onClick={() => close()}>Cancel</Button><Button primary onClick={accept}>{o.ok ?? (o.mode === "save" ? "Save" : "Select")}</Button></>}>
      <div className="row" style={{ padding: "10px 12px 6px" }}>
        <button className="tb-btn" title="Parent folder" onClick={() => parent && load(parent)}><ArrowUp size={16} /></button>
        <button className="tb-btn" title="Home" onClick={() => load("~")}><Home size={16} /></button>
        <input className="input grow mono" value={pathText} aria-label="Path" onChange={(e) => { typed.current = true; setPathText(e.target.value); }}
          onKeyDown={(e) => { if (e.key === "Enter") { e.stopPropagation(); e.preventDefault(); load(pathText); } }} />
      </div>
      {error && <div className="error-text" style={{ padding: "0 14px 6px" }}>{error}</div>}
      <div style={{ flex: 1, overflow: "auto", minHeight: 0, borderTop: "1px solid var(--border)" }}>
        <table className="grid">
          <thead><tr><th>Name</th><th style={{ width: 90 }}>Size</th><th style={{ width: 140 }}>Modified</th></tr></thead>
          <tbody>
            {shown.map((e) => (
              <tr key={e.path} className={sel.has(e.path) ? "sel" : ""} onClick={(ev) => click(e, ev)}
                onDoubleClick={() => (e.dir ? load(e.path) : o.mode === "save" ? (setName(e.name), close([e.path])) : o.mode !== "folder" && close([e.path]))}>
                <td><span className="row">{e.dir ? <Folder size={14} /> : <FileIcon size={14} />}{e.name}</span></td>
                <td>{e.dir ? "" : formatSize(e.size)}</td><td>{formatTime(e.mtime)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {o.mode === "save" && <div className="row" style={{ padding: "8px 12px" }}><span className="dim">File name</span><Text value={name} onChange={setName} /></div>}
    </Dialog>
  );
}

/** Returns the chosen absolute paths, or undefined when cancelled. */
export function pickPaths(o: PickOptions): Promise<string[] | undefined> {
  return showDialog<string[]>((close) => <Picker o={o} close={close} />);
}

export async function pickPath(o: PickOptions): Promise<string | undefined> {
  return (await pickPaths(o))?.[0];
}
