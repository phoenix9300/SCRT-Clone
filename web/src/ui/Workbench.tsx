// The main window: menu bar, toolbar, session manager, tab groups, status bar.
import {
  ArrowDownToLine, ChevronDown, ChevronRight, Copy, ClipboardPaste, FileText, Folder, FolderOpen, FolderTree, Lock, Monitor, Plug, PlugZap, Plus, RefreshCw,
  Search, Settings, SquareTerminal, Unplug, X, Zap, Network, Usb, Cable,
} from "lucide-react";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { rpc } from "../rpc";
import {
  Tab, TreeNode, activeController, activeTab, closeTab, connect, controllers, dismissToast, fail, focusTab, getApp, moveTab, onStatus, reloadTree, setApp,
  setGlobal, ui, useApp,
} from "../state/store";
import { emulationById } from "../term/emulation";
import { buildMenus, cmd, printText, runShortcut, terminalContextMenu } from "./commands";
import { confirm, openQuickConnect, promptText } from "./dialogs";
import { feature, tabViews } from "./features";
import { pickPath } from "./filePicker";
import { MenuItem, PopupMenu } from "./kit";
import { comboName, keyInfo } from "../term/keyboard";

// ------------------------------------------------------------------ menus

/** Gives the keyboard back to the terminal once a menu has closed (unless it opened a dialog). */
const refocusTerminal = () => setTimeout(() => {
  const a = document.activeElement;
  if (!getApp().dialogs.length && (!a || a === document.body || a.closest(".popup, .menubar"))) activeController()?.focus();
}, 0);

function MenuBar() {
  const [open, setOpen] = useState<{ i: number; x: number; y: number } | null>(null);
  const refs = useRef<(HTMLDivElement | null)[]>([]);
  const menus = useMemo(() => buildMenus(), [open]);
  const show = (i: number) => {
    const r = refs.current[i]!.getBoundingClientRect();
    setOpen({ i, x: r.left, y: r.bottom + 2 });
  };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.altKey && !e.ctrlKey && e.key.length === 1 && !getApp().dialogs.length) {
        const i = buildMenus().findIndex((m) => m.title[0].toLowerCase() === e.key.toLowerCase());
        // Alt+letter opens a menu only when the terminal is not using Alt as Meta
        if (i >= 0 && !activeController()) {
          e.preventDefault();
          show(i);
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return (
    <div className="menubar">
      <div className="brand"><SquareTerminal size={16} />Hawser</div>
      {menus.map((m, i) => (
        <div key={m.title} ref={(el) => { refs.current[i] = el; }} className={"menu-title" + (open?.i === i ? " open" : "")}
          onMouseDown={() => (open?.i === i ? setOpen(null) : show(i))} onMouseEnter={() => open && show(i)}>{m.title}</div>
      ))}
      {open && <PopupMenu key={open.i} x={open.x} y={open.y} items={menus[open.i].items} onClose={() => { setOpen(null); refocusTerminal(); }}
        onNavigate={(d) => show((open.i + d + menus.length) % menus.length)} />}
    </div>
  );
}

export function useContextMenu(): [React.ReactNode, (e: { clientX: number; clientY: number }, items: MenuItem[]) => void] {
  const [m, setM] = useState<{ x: number; y: number; items: MenuItem[] } | null>(null);
  return [m && <PopupMenu x={m.x} y={m.y} items={m.items} onClose={() => { setM(null); refocusTerminal(); }} />, (e, items) => setM({ x: e.clientX, y: e.clientY, items })];
}

function Toolbar() {
  const g = useApp((s) => s.global);
  const tab = useApp((s) => { const gr = s.groups.find((x) => x.id === s.activeGroup); return gr?.active ? s.tabs[gr.active] : null; });
  const [host, setHost] = useState("");
  const term = tab?.kind === "terminal";
  const up = term && tab!.info.state === "connected";
  const B = (p: { title: string; onClick: () => void; children: React.ReactNode; disabled?: boolean; on?: boolean }) => (
    <button className={"tb-btn" + (p.on ? " on" : "")} title={p.title} aria-label={p.title} disabled={p.disabled} onClick={p.onClick}>{p.children}</button>
  );
  const go = () => {
    const v = host.trim();
    if (!v) return;
    const m = /^(?:(ssh|telnet|rlogin|raw):\/\/)?(?:([^@]+)@)?([^:\s]+|\[[^\]]+\])(?::(\d+))?$/.exec(v);
    if (!m) return openQuickConnect(v);
    const protocol = m[1] === "ssh" || !m[1] ? "ssh2" : m[1];
    const ov: any = { protocol, host: m[3].replace(/^\[|\]$/g, "") };
    if (m[2]) ov.username = m[2];
    if (m[4]) ov.port = Number(m[4]);
    connect({ overrides: ov });
    setHost("");
  };
  return (
    <div className="toolbar">
      <B title="Session Manager" on={g.showSessionManager} onClick={cmd.toggleSessionManager}><FolderTree size={16} /></B>
      <B title="Quick Connect (Alt+Q)" onClick={cmd.quickConnect}><Zap size={16} /></B>
      <B title="Local Shell" onClick={cmd.connectLocal}><SquareTerminal size={16} /></B>
      <span className="tb-sep" />
      <B title="Reconnect" disabled={!term || up} onClick={cmd.reconnect}><RefreshCw size={16} /></B>
      <B title="Disconnect" disabled={!up} onClick={cmd.disconnect}><Unplug size={16} /></B>
      <span className="tb-sep" />
      <B title="Copy" disabled={!term} onClick={cmd.copy}><Copy size={16} /></B>
      <B title="Paste" disabled={!term} onClick={cmd.paste}><ClipboardPaste size={16} /></B>
      <B title="Find" disabled={!term} onClick={cmd.find}><Search size={16} /></B>
      <span className="tb-sep" />
      <B title="Log Session" disabled={!term} on={!!tab?.info.logging} onClick={cmd.toggleLog}><FileText size={16} /></B>
      <B title="SFTP Tab" disabled={!up || tab!.info.protocol !== "ssh2"} onClick={cmd.sftpTab}><ArrowDownToLine size={16} /></B>
      <B title="Session Options" onClick={cmd.sessionOptions}><Settings size={16} /></B>
      <div className="connect-bar">
        <Plug size={14} />
        <input value={host} placeholder="Enter host — [user@]host[:port]" spellCheck={false} list="recent-hosts" aria-label="Connect bar"
          onChange={(e) => setHost(e.target.value)} onKeyDown={(e) => { e.stopPropagation(); if (e.key === "Enter") go(); }} />
        <datalist id="recent-hosts">{g.recentHosts.map((h) => <option key={h} value={h} />)}</datalist>
      </div>
    </div>
  );
}

// -------------------------------------------------------- session manager

const protoIcon = (p?: string) => p === "serial" ? <Usb size={14} /> : p === "local" ? <SquareTerminal size={14} /> : p === "ssh2" ? <Monitor size={14} /> : p === "raw" ? <Cable size={14} /> : <Network size={14} />;

function matches(n: TreeNode, q: string): boolean {
  if (!q) return true;
  if (n.folder) return (n.children ?? []).some((c) => matches(c, q));
  const hay = (n.path + " " + (n.summary?.host ?? "") + " " + (n.summary?.username ?? "") + " " + (n.summary?.description ?? "")).toLowerCase();
  return hay.includes(q);
}

function SessionManager() {
  const tree = useApp((s) => s.tree);
  const selected = useApp((s) => s.selectedSession);
  const tabs = useApp((s) => s.tabs);
  const width = useApp((s) => s.global.sidebarWidth);
  const [filter, setFilter] = useState("");
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [menu, showMenu] = useContextMenu();
  const live = useMemo(() => new Set(Object.values(tabs).filter((t) => t.info.state === "connected").map((t) => t.info.session)), [tabs]);
  const q = filter.trim().toLowerCase();

  const newFolder = async (parent: string) => {
    const name = await promptText("New Folder", "Folder name:");
    if (name) rpc.call("sessions.mkdir", { path: parent ? parent + "/" + name : name }).catch(fail);
  };
  const rename = async (n: TreeNode) => {
    const name = await promptText("Rename", "New name:", n.name);
    if (!name || name === n.name) return;
    const parent = n.path.includes("/") ? n.path.slice(0, n.path.lastIndexOf("/") + 1) : "";
    rpc.call("sessions.rename", { from: n.path, to: parent + name, folder: n.folder }).catch(fail);
  };
  const remove = async (n: TreeNode) => {
    if (await confirm("Delete", n.folder ? `Delete folder "${n.path}" and every session in it?` : `Delete session "${n.path}"?`, "Delete", true))
      rpc.call("sessions.delete", { path: n.path, folder: n.folder }).catch(fail);
  };
  const duplicate = async (n: TreeNode) => {
    const name = await promptText("Copy Session", "Name of the copy:", n.path + " copy");
    if (name) rpc.call("sessions.copy", { from: n.path, to: name }).catch(fail);
  };
  const ctx = (e: React.MouseEvent, n: TreeNode | null) => {
    e.preventDefault();
    e.stopPropagation();
    if (n) setApp({ selectedSession: n.path });
    const folder = !n ? "" : n.folder ? n.path : n.path.includes("/") ? n.path.slice(0, n.path.lastIndexOf("/")) : "";
    const items: MenuItem[] = [];
    if (n && !n.folder) items.push(
      { label: "Connect", run: () => void connect({ session: n.path }) },
      { label: "Connect SFTP", disabled: n.summary?.protocol !== "ssh2", run: () => feature("sftpOpen", { session: n.path }) },
      { sep: true }, { label: "Properties…", run: () => feature("sessionOptions", { path: n.path }) },
      { label: "Duplicate…", run: () => duplicate(n) },
      { label: "Open at Startup", checked: (getApp().global.autoSessions ?? []).includes(n.path), run: () => {
        const cur = getApp().global.autoSessions ?? [];
        setGlobal({ autoSessions: cur.includes(n.path) ? cur.filter((x) => x !== n.path) : [...cur, n.path] });
      } });
    if (n?.folder) items.push({ label: "Connect All", run: () => (n.children ?? []).filter((c) => !c.folder).forEach((c) => void connect({ session: c.path })) }, { sep: true });
    if (n) items.push({ label: "Rename…", run: () => rename(n) }, { label: "Delete…", run: () => remove(n) }, { sep: true });
    items.push({ label: "New Session…", run: () => feature("sessionOptions", { isNew: true, folder }) }, { label: "New Folder…", run: () => newFolder(folder) },
      { sep: true }, { label: "Import Sessions…", run: () => feature("importSessions") });
    showMenu(e, items);
  };
  const onDrop = (e: React.DragEvent, folder: string) => {
    e.preventDefault();
    const raw = e.dataTransfer.getData("application/x-hawser-session");
    if (!raw) return;
    const src = JSON.parse(raw) as { path: string; folder: boolean; name: string };
    const to = folder ? folder + "/" + src.name : src.name;
    if (to !== src.path) rpc.call("sessions.rename", { from: src.path, to, folder: src.folder }).catch(fail);
  };
  const rows: React.ReactNode[] = [];
  const walk = (n: TreeNode, depth: number) => {
    for (const c of n.children ?? []) {
      if (!matches(c, q)) continue;
      const isCollapsed = !q && collapsed[c.path];
      rows.push(
        <div key={(c.folder ? "d:" : "s:") + c.path} className={"tree-row" + (selected === c.path ? " sel" : "")} style={{ paddingLeft: 6 + depth * 14 }}
          draggable onDragStart={(e) => e.dataTransfer.setData("application/x-hawser-session", JSON.stringify({ path: c.path, folder: c.folder, name: c.name }))}
          onDragOver={(e) => c.folder && e.preventDefault()} onDrop={(e) => c.folder && onDrop(e, c.path)}
          onClick={() => { setApp({ selectedSession: c.path }); if (c.folder) setCollapsed({ ...collapsed, [c.path]: !collapsed[c.path] }); }}
          onDoubleClick={() => !c.folder && void connect({ session: c.path })} onContextMenu={(e) => ctx(e, c)}
          title={c.folder ? c.path : `${c.summary?.protocol} ${c.summary?.username ? c.summary.username + "@" : ""}${c.summary?.host ?? ""}${c.summary?.description ? "\n" + c.summary.description : ""}`}>
          {c.folder ? (isCollapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />) : <span style={{ width: 14 }} />}
          {c.folder ? (isCollapsed ? <Folder size={14} /> : <FolderOpen size={14} />) : protoIcon(c.summary?.protocol)}
          <span className="name">{c.name}</span>
          {!c.folder && <span className="sub">{c.summary?.host}</span>}
          {!c.folder && live.has(c.path) && <span className="dot" title="Connected" />}
        </div>,
      );
      if (c.folder && !isCollapsed) walk(c, depth + 1);
    }
  };
  if (tree) walk(tree, 0);
  const startResize = (e: React.MouseEvent) => {
    e.preventDefault();
    const x0 = e.clientX;
    const w0 = width;
    const move = (ev: MouseEvent) => setApp((s) => ({ global: { ...s.global, sidebarWidth: Math.min(600, Math.max(160, w0 + ev.clientX - x0)) } }));
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      setGlobal({ sidebarWidth: getApp().global.sidebarWidth });
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };
  return (
    <>
      <div className="sidebar" style={{ width }}>
        <div className="side-head">
          <span>Sessions</span>
          <button className="tb-btn" title="New Session" onClick={() => feature("sessionOptions", { isNew: true, folder: "" })}><Plus size={15} /></button>
          <button className="tb-btn" title="Refresh" onClick={() => reloadTree().catch(fail)}><RefreshCw size={14} /></button>
        </div>
        <input className="input side-filter" placeholder="Filter sessions" value={filter} aria-label="Filter sessions" onChange={(e) => setFilter(e.target.value)} onKeyDown={(e) => e.stopPropagation()} />
        <div className="tree" onContextMenu={(e) => ctx(e, null)} onDragOver={(e) => e.preventDefault()} onDrop={(e) => onDrop(e, "")}>
          {rows.length ? rows : <div className="empty-hint">{q ? "No sessions match the filter." : "No saved sessions yet. Use Quick Connect and tick “Save session”, or right-click here to create one."}</div>}
        </div>
      </div>
      <div className="splitter" onMouseDown={startResize} />
      {menu}
    </>
  );
}

// ------------------------------------------------------------------- tabs

function TerminalPane({ tab, active }: { tab: Tab; active: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const c = controllers.get(tab.id);
    if (!c || !ref.current) return;
    c.attach(ref.current);
    return () => c.detach();
  }, [tab.id]);
  useEffect(() => {
    if (active) {
      const c = controllers.get(tab.id);
      c?.layout();
      if (!getApp().dialogs.length) c?.focus();
    }
  }, [active]);
  const off = tab.info.state === "disconnected";
  return (
    <>
      <div ref={ref} style={{ flex: 1, minHeight: 0, display: "flex" }} />
      {active && <FindBar tabId={tab.id} />}
      {tab.locked && <div className="overlay-msg"><Lock size={15} />Session locked<button className="btn small" onClick={cmd.lock}>Unlock…</button></div>}
      {off && !tab.locked && <div className="overlay-msg"><PlugZap size={15} />{tab.info.message || "Disconnected"}
        <button className="btn small primary" onClick={() => rpc.call("conn.reconnect", { id: tab.connId }).catch(fail)}>Reconnect</button></div>}
    </>
  );
}

function FindBar({ tabId }: { tabId: string }) {
  const open = useApp((s) => s.findOpen);
  const [q, setQ] = useState("");
  const [cs, setCs] = useState(false);
  const [re, setRe] = useState(false);
  const [miss, setMiss] = useState(false);
  const c = controllers.get(tabId);
  useEffect(() => {
    if (!c) return;
    if (!open) c.setSearch("", { caseSensitive: false, regex: false });
    else setMiss(!c.setSearch(q, { caseSensitive: cs, regex: re }));
  }, [open, q, cs, re]);
  if (!open || !c) return null;
  const close = () => { setApp({ findOpen: false }); c.focus(); };
  const go = (back: boolean) => setMiss(!c.find(back) && !!q);
  return (
    <div className="findbar" onKeyDown={(e) => { e.stopPropagation(); if (e.key === "Escape") close(); else if (e.key === "Enter") go(!e.shiftKey); }}>
      <input type="text" className={"input" + (miss ? " miss" : "")} autoFocus placeholder="Find in scrollback" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Find" />
      <button className={"tb-btn" + (cs ? " on" : "")} title="Match case" onClick={() => setCs(!cs)}>Aa</button>
      <button className={"tb-btn" + (re ? " on" : "")} title="Regular expression" onClick={() => setRe(!re)}>.*</button>
      <button className="tb-btn" title="Previous (Enter)" onClick={() => go(true)}>↑</button>
      <button className="tb-btn" title="Next (Shift+Enter)" onClick={() => go(false)}>↓</button>
      <button className="tb-btn" title="Close" onClick={close}><X size={15} /></button>
    </div>
  );
}

function GroupView({ id }: { id: string }) {
  const group = useApp((s) => s.groups.find((g) => g.id === id))!;
  const tabs = useApp((s) => s.tabs);
  const focused = useApp((s) => s.activeGroup === id);
  const [menu, showMenu] = useContextMenu();
  const [dropAt, setDropAt] = useState<string | null>(null);
  const tabMenu = (e: React.MouseEvent, t: Tab) => {
    e.preventDefault();
    focusTab(t.id);
    const term = t.kind === "terminal";
    showMenu(e, [
      { label: "Reconnect", disabled: !term || t.info.state !== "disconnected", run: cmd.reconnect },
      { label: "Disconnect", disabled: !term || t.info.state === "disconnected", run: cmd.disconnect },
      { label: "Clone Session", disabled: !term, run: cmd.clone }, { sep: true },
      { label: "Rename…", run: cmd.renameTab }, { label: "Session Options…", disabled: !term, run: cmd.sessionOptions }, { sep: true },
      { label: "Move to New Tab Group (Right)", run: cmd.splitRight }, { label: "Move to New Tab Group (Below)", run: cmd.splitDown }, { sep: true },
      { label: "Close", run: cmd.closeTab },
      { label: "Close Others", run: () => group.tabs.filter((x) => x !== t.id).forEach((x) => void closeTab(x)) },
    ]);
  };
  const drop = (e: React.DragEvent, before?: string) => {
    e.preventDefault();
    setDropAt(null);
    const tid = e.dataTransfer.getData("application/x-hawser-tab");
    if (tid && tid !== before) moveTab(tid, id, before);
  };
  return (
    <div className={"group" + (focused ? " focus" : "")} onMouseDownCapture={() => !focused && setApp({ activeGroup: id })}>
      <div className="tabstrip" onDragOver={(e) => e.preventDefault()} onDrop={(e) => drop(e)} onDoubleClick={(e) => e.target === e.currentTarget && cmd.connectLocal()}>
        {group.tabs.map((tid) => {
          const t = tabs[tid];
          if (!t) return null;
          const st = t.info.state === "connected" ? (t.activity ? "activity" : "connected") : t.info.state === "connecting" ? "connecting" : t.info.failed ? "failed" : "";
          const color = t.info.options?.appearance?.tabColor;
          return (
            <div key={tid} className={"tab" + (group.active === tid ? " active" : "") + (dropAt === tid ? " drop" : "")} style={color ? ({ "--tab-color": color } as React.CSSProperties) : undefined}
              draggable onDragStart={(e) => e.dataTransfer.setData("application/x-hawser-tab", tid)}
              onDragOver={(e) => { e.preventDefault(); setDropAt(tid); }} onDragLeave={() => setDropAt(null)} onDrop={(e) => { e.stopPropagation(); drop(e, tid); }}
              onMouseDown={(e) => e.button === 0 && focusTab(tid)} onAuxClick={(e) => e.button === 1 && void closeTab(tid)} onContextMenu={(e) => tabMenu(e, t)}
              title={`${t.info.target}${t.info.message ? "\n" + t.info.message : ""}`}>
              <span className={"state " + st} />
              <span className="title">{t.bell ? "🔔 " : ""}{t.title}</span>
              <button className="x" aria-label="Close tab" onMouseDown={(e) => e.stopPropagation()} onClick={() => void closeTab(tid)}><X size={13} /></button>
            </div>
          );
        })}
        <button className="tab-add" title="New local shell tab" onClick={cmd.connectLocal}><Plus size={15} /></button>
      </div>
      <div className="pane">
        {group.tabs.length === 0 && <Welcome />}
        {group.tabs.map((tid) => {
          const t = tabs[tid];
          if (!t) return null;
          const View = t.kind === "terminal" ? TerminalPane : tabViews[t.kind];
          return <div key={tid} data-view={tid} className={"view" + (group.active === tid ? " active" : "")}>{View ? <View tab={t} active={group.active === tid && focused} /> : null}</div>;
        })}
      </div>
      {menu}
    </div>
  );
}

function Welcome() {
  return (
    <div className="welcome" style={{ height: "100%" }}>
      <div>
        <h1>Hawser</h1>
        <div>Secure terminal sessions over SSH2, Telnet, Rlogin, serial lines and local shells.</div>
        <div className="keys">
          <kbd>Alt+Q</kbd><span>Quick Connect</span>
          <kbd>Ctrl+Shift+T</kbd><span>Local shell</span>
          <kbd>Alt+C</kbd><span>Show or hide the session manager</span>
          <kbd>Ctrl+Shift+F</kbd><span>Find in scrollback</span>
        </div>
      </div>
    </div>
  );
}

// ------------------------------------------------- button bar, command window

function ButtonBar() {
  const g = useApp((s) => s.global);
  const bar = g.buttonBars?.[g.activeButtonBar] ?? [];
  const [menu, showMenu] = useContextMenu();
  const run = (b: { action: string; value: string }) => {
    const t = activeTab();
    if (b.action === "send" && t?.kind === "terminal") rpc.notify("conn.send", { id: t.connId, text: b.value, escapes: true });
    else if (b.action === "script") feature("scriptRun", { conn: t?.connId, path: b.value });
    else if (b.action === "menu") cmd[b.value]?.();
    activeController()?.focus();
  };
  return (
    <div className="buttonbar" onContextMenu={(e) => { e.preventDefault(); showMenu(e, [{ label: "Edit Button Bar…", run: () => feature("buttonBarEditor") },
      ...Object.keys(g.buttonBars ?? {}).map((n) => ({ label: n, checked: n === g.activeButtonBar, run: () => setGlobal({ activeButtonBar: n }) }))]); }}>
      {bar.length === 0 && <span className="dim">No buttons yet — right-click to edit the button bar.</span>}
      {bar.map((b, i) => <button key={i} className="btn small" style={b.color ? { borderColor: b.color, color: b.color } : undefined} title={b.value} onClick={() => run(b)}>{b.label}</button>)}
      {menu}
    </div>
  );
}

function CommandWindow() {
  const [text, setText] = useState("");
  const [target, setTarget] = useState<"active" | "all" | "group">("active");
  const hist = useRef<string[]>([]);
  const pos = useRef(-1);
  const send = () => {
    const s = getApp();
    let ids: number[] = [];
    const term = (id: string) => s.tabs[id]?.kind === "terminal" && s.tabs[id].info.state === "connected";
    if (target === "active") ids = [activeTab()].filter((t) => t && term(t.id)).map((t) => t!.connId);
    else if (target === "group") ids = (s.groups.find((g) => g.id === s.activeGroup)?.tabs ?? []).filter(term).map((id) => s.tabs[id].connId);
    else ids = Object.keys(s.tabs).filter(term).map((id) => s.tabs[id].connId);
    if (!ids.length) return;
    rpc.notify("conn.send", { ids, text: text.replace(/\r?\n/g, "\r") + "\r" });
    if (text) hist.current.unshift(text);
    pos.current = -1;
    setText("");
  };
  return (
    <div className="cmdwin">
      <select className="input" style={{ width: 150 }} value={target} onChange={(e) => setTarget(e.target.value as any)} aria-label="Send commands to">
        <option value="active">Active session</option><option value="group">Sessions in tab group</option><option value="all">All sessions</option>
      </select>
      <textarea className="input" value={text} placeholder="Type a command and press Enter to send (Shift+Enter for a new line)" aria-label="Command window"
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "Enter" && !e.shiftKey) (e.preventDefault(), send());
          else if (e.key === "ArrowUp" && !text.includes("\n") && hist.current.length) { pos.current = Math.min(pos.current + 1, hist.current.length - 1); setText(hist.current[pos.current]); }
          else if (e.key === "ArrowDown" && pos.current >= 0) { pos.current--; setText(pos.current < 0 ? "" : hist.current[pos.current]); }
          else if (e.key === "Escape") activeController()?.focus();
        }} />
    </div>
  );
}

function StatusBar() {
  const [, tick] = useState(0);
  const tab = useApp((s) => { const gr = s.groups.find((x) => x.id === s.activeGroup); return gr?.active ? s.tabs[gr.active] : null; });
  useEffect(() => onStatus(() => tick((n) => n + 1)), []);
  const c = tab ? controllers.get(tab.id) : null;
  const d = tab?.info.details ?? {};
  const state = !tab ? "Ready" : tab.info.state === "connected" ? `Connected to ${tab.info.target}` : tab.info.message || tab.info.state;
  const sec = tab?.info.state !== "connected" ? "" : tab.info.protocol === "ssh2" ? `ssh2: ${d.cipherOut ?? ""}` : d.tlsVersion ? `${tab.info.protocol}: ${d.tlsVersion}` : tab.info.protocol;
  useEffect(() => {
    if (tab?.kind === "terminal" && tab.info.state === "connected" && !tab.info.details) rpc.call("conn.info", { id: tab.connId }).then((i) => setApp((s) => s.tabs[tab.id] ? { tabs: { ...s.tabs, [tab.id]: { ...s.tabs[tab.id], info: i } } } : {})).catch(() => {});
  }, [tab?.id, tab?.info.state]);
  return (
    <div className="statusbar">
      <div className="cell">{state}</div>
      {sec && <div className="cell">{sec}</div>}
      {c && <div className="cell">{c.term.buf.y + 1}, {c.term.buf.x + 1}</div>}
      {c && <div className="cell">{c.term.rows} Rows, {c.term.cols} Cols</div>}
      {c && <div className="cell">{emulationById(c.opts.terminal.emulation).label}</div>}
      {tab?.info.logging && <div className="cell on" title={tab.info.logFile}>LOG</div>}
      {tab?.info.recording && <div className="cell rec">REC</div>}
      {c && c.term.buf.scrollOffset > 0 && <div className="cell on">SCROLL −{c.term.buf.scrollOffset}</div>}
    </div>
  );
}

function Toasts() {
  const toasts = useApp((s) => s.toasts);
  return (
    <div className="toasts">
      {toasts.map((t) => (
        <div key={t.id} className={"toast " + t.kind} onClick={() => dismissToast(t.id)}>
          {t.text}
          {t.progress !== undefined && <div className="bar"><i style={{ width: Math.round(t.progress * 100) + "%" }} /></div>}
        </div>
      ))}
    </div>
  );
}

export function Workbench() {
  const g = useApp((s) => s.global);
  const groups = useApp((s) => s.groups);
  const split = useApp((s) => s.split);
  const dialogs = useApp((s) => s.dialogs);
  const [menu, showMenu] = useContextMenu();
  useEffect(() => {
    ui.shortcut = (combo, e, c) => runShortcut(combo, e, c);
    ui.contextMenu = (e) => showMenu(e, terminalContextMenu());
    // printer pass-through from the host: print dialog, save to a file, or drop
    ui.print = (text) => {
      const mode = activeController()?.opts.terminal.printer ?? "dialog";
      if (mode === "dialog") printText(text);
      else if (mode === "file") pickPath({ mode: "save", title: "Save Printer Output", defaultName: "print.txt" }).then((p) => p && rpc.call("fs.writeText", { path: p, data: text }).catch(fail));
    };
    // shortcuts also work when focus is outside a terminal
    const onKey = (e: KeyboardEvent) => {
      if (getApp().dialogs.length || (e.target as HTMLElement).closest?.(".term-root")) return;
      const tag = (e.target as HTMLElement).tagName;
      if ((tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") && !e.ctrlKey && !e.altKey && !e.metaKey) return;
      if (runShortcut(comboName(keyInfo(e)), e, null)) e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    const unload = (e: BeforeUnloadEvent) => {
      if (getApp().global.confirmExit && Object.values(getApp().tabs).some((t) => t.info.state === "connected")) e.preventDefault();
    };
    window.addEventListener("beforeunload", unload);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("beforeunload", unload);
    };
  }, []);
  return (
    <div className="app">
      <MenuBar />
      {g.showToolbar && <Toolbar />}
      <div className="body">
        {g.showSessionManager && <SessionManager />}
        <div className="main">
          <div className={"groups split-" + split}>{groups.map((gr) => <GroupView key={gr.id} id={gr.id} />)}</div>
          {g.showButtonBar && <ButtonBar />}
          {g.showCommandWindow && <CommandWindow />}
        </div>
      </div>
      {g.showStatusBar && <StatusBar />}
      {dialogs.map((d) => <React.Fragment key={d.id}>{d.node}</React.Fragment>)}
      <Toasts />
      {menu}
    </div>
  );
}
