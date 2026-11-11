// Menu definitions and keyboard shortcuts.
import { rpc } from "../rpc";
import {
  activeController, activeTab, closeTab, connect, controllers, cycleTab, fail, focusTab, getApp, patchTab, setApp, setGlobal, splitActive, toast, ui,
} from "../state/store";
import { TerminalController } from "../view/controller";
import { openAbout, openQuickConnect, promptText } from "./dialogs";
import { feature } from "./features";
import { MenuItem } from "./kit";

const isMac = navigator.platform.startsWith("Mac");

function conn(): { id: number; connected: boolean } | null {
  const t = activeTab();
  return t && t.kind === "terminal" ? { id: t.connId, connected: t.info.state === "connected" } : null;
}

function download(name: string, text: string): void {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

export function printText(text: string): void {
  const w = window.open("", "_blank", "width=800,height=600");
  if (!w) return void toast("The print window was blocked.", "error");
  w.document.title = "Hawser print";
  const pre = w.document.createElement("pre");
  pre.style.cssText = "font:11px/1.25 monospace;white-space:pre-wrap";
  pre.textContent = text;
  w.document.body.appendChild(pre);
  w.print();
}

export const cmd: Record<string, () => void> = {
  quickConnect: () => openQuickConnect(),
  connectLocal: () => void connect({ overrides: { protocol: "local" } }),
  newSession: () => feature("sessionOptions", { isNew: true, folder: "" }),
  sessionOptions: () => {
    const t = activeTab();
    if (t?.info.session) feature("sessionOptions", { path: t.info.session, connId: t.connId });
    else if (t) feature("sessionOptions", { connId: t.connId, adhoc: true });
    else feature("sessionOptions", { path: "" });
  },
  defaultSession: () => feature("sessionOptions", { path: "" }),
  globalOptions: () => feature("globalOptions"),
  reconnect: () => {
    const c = conn();
    if (c) rpc.call("conn.reconnect", { id: c.id }).catch(fail);
  },
  reconnectAll: () => Object.values(getApp().tabs).forEach((t) => t.kind === "terminal" && t.info.state === "disconnected" && rpc.call("conn.reconnect", { id: t.connId }).catch(() => {})),
  disconnect: () => {
    const c = conn();
    if (c) rpc.call("conn.disconnect", { id: c.id }).catch(fail);
  },
  disconnectAll: () => Object.values(getApp().tabs).forEach((t) => t.kind === "terminal" && rpc.call("conn.disconnect", { id: t.connId }).catch(() => {})),
  clone: () => {
    const t = activeTab();
    if (t?.kind === "terminal") void connect({ cloneOf: t.connId, title: t.info.title });
  },
  sftpTab: () => {
    const t = activeTab();
    if (t?.kind === "terminal") feature("sftpOpen", { conn: t.connId });
  },
  lock: async () => {
    const t = activeTab();
    const c = activeController();
    if (!t || !c) return;
    if (t.locked) {
      const pw = await promptText("Unlock Session", "Password:", "", { secret: true });
      if (pw === undefined) return;
      if (pw !== (c as any).lockPassword) return void toast("Incorrect password.", "error");
      c.locked = false;
      patchTab(t.id, { locked: false });
    } else {
      const pw = await promptText("Lock Session", "Password to unlock (may be empty):", "", { secret: true });
      if (pw === undefined) return;
      (c as any).lockPassword = pw;
      c.locked = true;
      patchTab(t.id, { locked: true });
    }
  },
  toggleLog: () => {
    const t = activeTab();
    if (!t || t.kind !== "terminal") return;
    if (t.info.logging) rpc.call("conn.log.stop", { id: t.connId }).catch(fail);
    else if (t.info.options?.log?.promptForFile) feature("logStart", { conn: t.connId });
    else rpc.call<string>("conn.log.start", { id: t.connId }).then((p) => toast("Logging to " + p)).catch(fail);
  },
  rawLog: () => {
    const t = activeTab();
    if (t?.kind === "terminal") rpc.call<string>("conn.log.start", { id: t.connId, raw: true }).then((p) => toast("Raw logging to " + p)).catch(fail);
  },
  printScreen: () => {
    const c = activeController();
    if (c) printText(c.term.screenText().join("\n"));
  },
  printSelection: () => {
    const c = activeController();
    if (c) printText(c.selectionText() || c.term.screenText().join("\n"));
  },
  saveScrollback: () => {
    const c = activeController();
    const t = activeTab();
    if (c && t) download(t.title.replace(/[^\w.-]+/g, "_") + ".txt", c.allText());
  },
  exit: async () => {
    const live = Object.values(getApp().tabs).filter((t) => t.info.state === "connected").length;
    if (live && getApp().global.confirmExit && !(await ui.confirm("Exit Hawser", `${live} session(s) are connected. Disconnect them and exit?`, "Exit"))) return;
    await rpc.call("app.quit").catch(() => {});
    window.close();
    document.body.innerHTML = '<div class="boot"><div class="card">Hawser has shut down. You can close this window.</div></div>';
  },
  copy: () => void activeController()?.copy(),
  paste: () => void activeController()?.paste(),
  copyPaste: async () => {
    const c = activeController();
    if (c && (await c.copy())) c.paste(c.selectionText());
  },
  selectAll: () => activeController()?.selectAll(),
  find: () => setApp({ findOpen: true }),
  clearScrollback: () => activeController()?.term.clearScrollback(),
  clearScreen: () => activeController()?.clearScreen(false),
  clearAll: () => activeController()?.clearScreen(true),
  sendBreak: () => {
    const c = conn();
    if (c) rpc.call("conn.break", { id: c.id }).catch(fail);
  },
  reset: () => activeController()?.reset(),
  toggleSessionManager: () => setGlobal({ showSessionManager: !getApp().global.showSessionManager }),
  toggleToolbar: () => setGlobal({ showToolbar: !getApp().global.showToolbar }),
  toggleStatusBar: () => setGlobal({ showStatusBar: !getApp().global.showStatusBar }),
  toggleButtonBar: () => setGlobal({ showButtonBar: !getApp().global.showButtonBar }),
  toggleCommandWindow: () => setGlobal({ showCommandWindow: !getApp().global.showCommandWindow }),
  zoomIn: () => setGlobal({ fontSize: Math.min(40, getApp().global.fontSize + 1) }),
  zoomOut: () => setGlobal({ fontSize: Math.max(6, getApp().global.fontSize - 1) }),
  zoomReset: () => setGlobal({ fontSize: 14 }),
  fullScreen: () => {
    if (document.fullscreenElement) document.exitFullscreen();
    else document.documentElement.requestFullscreen().then(() => (navigator as any).keyboard?.lock?.().catch(() => {})).catch(() => {});
  },
  nextTab: () => cycleTab(1),
  prevTab: () => cycleTab(-1),
  closeTab: () => {
    const t = activeTab();
    if (t) void closeTab(t.id);
  },
  renameTab: async () => {
    const t = activeTab();
    if (!t) return;
    const v = await promptText("Rename Tab", "Tab label:", t.title);
    if (v !== undefined) patchTab(t.id, { title: v || t.info.title, customTitle: v || undefined });
  },
  splitRight: () => splitActive("row"),
  splitDown: () => splitActive("column"),
  scrollTop: () => activeController()?.scrollToTop(),
  scrollBottom: () => activeController()?.scrollToBottom(),
  about: openAbout,
  startRecording: () => {
    const c = conn();
    if (c) rpc.call("conn.record.start", { id: c.id }).catch(fail);
  },
  stopRecording: () => {
    const c = conn();
    if (c) feature("recordStop", { conn: c.id });
  },
};

/** Default keyboard shortcuts (combo -> command). */
export const DEFAULT_SHORTCUTS: Record<string, string> = {
  "Alt+Q": "quickConnect", "Alt+C": "toggleSessionManager", "Ctrl+Shift+T": "connectLocal", "Ctrl+Shift+R": "reconnect",
  "Ctrl+Shift+C": "copy", "Ctrl+Insert": "copy", "Ctrl+Shift+V": "paste", "Shift+Insert": "paste", "Ctrl+Shift+A": "selectAll",
  "Ctrl+Shift+F": "find", "Ctrl+Tab": "nextTab", "Ctrl+Shift+Tab": "prevTab", "Ctrl+PageDown": "nextTab", "Ctrl+PageUp": "prevTab",
  "Ctrl+Shift+W": "closeTab", "Ctrl+F4": "closeTab", "Ctrl+=": "zoomIn", "Ctrl+Shift++": "zoomIn", "Ctrl+-": "zoomOut", "Ctrl+0": "zoomReset",
  "F11": "fullScreen", "Alt+Enter": "fullScreen", "Ctrl+Shift+Home": "scrollTop", "Ctrl+Shift+End": "scrollBottom",
  "Ctrl+Shift+B": "sendBreak", "Ctrl+Shift+D": "clone", "Ctrl+Shift+L": "toggleLog",
  ...(isMac ? { "Meta+C": "copy", "Meta+V": "paste", "Meta+F": "find", "Meta+T": "connectLocal", "Meta+W": "closeTab", "Meta+A": "selectAll" } : {}),
};

function keysFor(command: string): string {
  const user = getApp().global.shortcuts ?? {};
  for (const [k, c] of Object.entries(user)) if (c === command) return k;
  for (const [k, c] of Object.entries(DEFAULT_SHORTCUTS)) if (c === command && user[k] === undefined) return k;
  return "";
}

export function runShortcut(combo: string, _e: KeyboardEvent | null, _c: TerminalController | null): boolean {
  if (combo === "@reconnect") return cmd.reconnect(), true;
  if (combo === "@zoomIn") return cmd.zoomIn(), true;
  if (combo === "@zoomOut") return cmd.zoomOut(), true;
  const user = getApp().global.shortcuts ?? {};
  let id = user[combo];
  if (id === undefined) id = DEFAULT_SHORTCUTS[combo];
  if (id === undefined && /^Alt\+[1-9]$/.test(combo)) {
    const s = getApp();
    const g = s.groups.find((x) => x.id === s.activeGroup);
    const t = g?.tabs[Number(combo.slice(4)) - 1];
    if (t) focusTab(t);
    return !!t;
  }
  if (!id || id === "none") return false;
  if (id.startsWith("send:")) {
    const c = conn();
    if (c) rpc.notify("conn.send", { id: c.id, text: id.slice(5), escapes: true });
    return true;
  }
  const fn = cmd[id];
  if (!fn) return false;
  fn();
  return true;
}

const it = (label: string, command: string, extra: Partial<MenuItem> = {}): MenuItem => ({ label, keys: keysFor(command), run: cmd[command], ...extra });
const sep: MenuItem = { sep: true };

export function buildMenus(): { title: string; items: MenuItem[] }[] {
  const s = getApp();
  const g = s.global;
  const t = activeTab();
  const term = t?.kind === "terminal";
  const up = term && t!.info.state === "connected";
  const off = !term;
  const ssh = up && t!.info.protocol === "ssh2";
  const ctl = activeController();
  return [
    { title: "File", items: [
      it("Quick Connect…", "quickConnect"), it("New Session…", "newSession"), it("Local Shell", "connectLocal"), sep,
      it("Reconnect", "reconnect", { disabled: !term || up }), it("Reconnect All", "reconnectAll"),
      it("Disconnect", "disconnect", { disabled: !term || t!.info.state === "disconnected" }), it("Disconnect All", "disconnectAll"), sep,
      it("Clone Session", "clone", { disabled: off }), it("Connect SFTP Tab", "sftpTab", { disabled: !ssh }),
      it(t?.locked ? "Unlock Session…" : "Lock Session…", "lock", { disabled: off }), sep,
      it("Log Session", "toggleLog", { disabled: off, checked: !!t?.info.logging }), it("Raw Log Session", "rawLog", { disabled: off }),
      it("Save Scrollback to File…", "saveScrollback", { disabled: off }), sep,
      it("Print Screen…", "printScreen", { disabled: off }), it("Print Selection…", "printSelection", { disabled: off }), sep,
      it("Exit", "exit"),
    ] },
    { title: "Edit", items: [
      it("Copy", "copy", { disabled: !ctl?.hasSelection() }), it("Paste", "paste", { disabled: off }), it("Copy and Paste", "copyPaste", { disabled: !ctl?.hasSelection() }),
      it("Select All", "selectAll", { disabled: off }), it("Find…", "find", { disabled: off }), sep,
      it("Clear Scrollback", "clearScrollback", { disabled: off }), it("Clear Screen", "clearScreen", { disabled: off }), it("Clear Screen and Scrollback", "clearAll", { disabled: off }), sep,
      it("Send Break", "sendBreak", { disabled: !up }), it("Reset Terminal", "reset", { disabled: off }),
    ] },
    { title: "View", items: [
      it("Session Manager", "toggleSessionManager", { checked: g.showSessionManager }), it("Toolbar", "toggleToolbar", { checked: g.showToolbar }),
      it("Status Bar", "toggleStatusBar", { checked: g.showStatusBar }), it("Button Bar", "toggleButtonBar", { checked: g.showButtonBar }),
      it("Command Window", "toggleCommandWindow", { checked: g.showCommandWindow }), sep,
      { label: "Theme", children: (["dark", "light", "system"] as const).map((th) => ({ label: th[0].toUpperCase() + th.slice(1), checked: g.theme === th, run: () => setGlobal({ theme: th }) })) },
      it("Zoom In", "zoomIn"), it("Zoom Out", "zoomOut"), it("Reset Zoom", "zoomReset"), it("Full Screen", "fullScreen"), sep,
      it("Scroll to Top", "scrollTop", { disabled: off }), it("Scroll to Bottom", "scrollBottom", { disabled: off }),
    ] },
    { title: "Options", items: [
      it("Session Options…", "sessionOptions"), it("Default Session…", "defaultSession"), it("Global Options…", "globalOptions"), sep,
      { label: "Keyword Highlighting…", run: () => feature("keywords") }, { label: "Button Bar…", run: () => feature("buttonBarEditor") },
      { label: "Keyboard Shortcuts…", run: () => feature("shortcuts") },
    ] },
    { title: "Transfer", items: [
      { label: "Send ASCII…", disabled: !up, run: () => feature("xferStart", { conn: t!.connId, protocol: "ascii", direction: "upload" }) }, sep,
      { label: "Zmodem Upload…", disabled: !up, run: () => feature("xferStart", { conn: t!.connId, protocol: "zmodem", direction: "upload" }) },
      { label: "Ymodem Upload…", disabled: !up, run: () => feature("xferStart", { conn: t!.connId, protocol: "ymodem", direction: "upload" }) },
      { label: "Ymodem Download", disabled: !up, run: () => rpc.call("xfer.start", { id: t!.connId, protocol: "ymodem", direction: "download" }).catch(fail) },
      { label: "Xmodem Upload…", disabled: !up, run: () => feature("xferStart", { conn: t!.connId, protocol: "xmodem", direction: "upload" }) },
      { label: "Xmodem Download…", disabled: !up, run: () => feature("xferStart", { conn: t!.connId, protocol: "xmodem", direction: "download" }) }, sep,
      it("Connect SFTP Tab", "sftpTab", { disabled: !ssh }), { label: "TFTP Server…", run: () => feature("tftp") },
    ] },
    { title: "Script", items: [
      { label: "Run…", disabled: off, run: () => feature("scriptRun", { conn: t?.connId }) }, { label: "Cancel", disabled: off, run: () => feature("scriptStop", { conn: t?.connId }) }, sep,
      it("Start Recording Script", "startRecording", { disabled: !up || !!t?.info.recording }), it("Stop Recording Script…", "stopRecording", { disabled: !t?.info.recording }),
    ] },
    { title: "Tools", items: [
      { label: "Create Public Key…", run: () => feature("keygen") }, { label: "Convert Private Key…", run: () => feature("keyConvert") },
      { label: "Manage Agent Keys…", run: () => feature("agentKeys") }, { label: "Host Key Database…", run: () => feature("hostKeys") }, sep,
      { label: "Port Forwarding…", disabled: !ssh, run: () => feature("forwards", { conn: t!.connId }) },
      { label: "Connection Details…", disabled: off, run: () => feature("connInfo", { conn: t?.connId }) }, sep,
      { label: "Credential Vault…", run: () => feature("vault") },
      { label: "Import Sessions…", run: () => feature("importSessions") }, { label: "Export Sessions…", run: () => feature("exportSessions") },
    ] },
    { title: "Window", items: [
      it("Next Tab", "nextTab"), it("Previous Tab", "prevTab"), it("Rename Tab…", "renameTab", { disabled: !t }), it("Close Tab", "closeTab", { disabled: !t }), sep,
      it("Move to New Tab Group (Right)", "splitRight", { disabled: !t }), it("Move to New Tab Group (Below)", "splitDown", { disabled: !t }), sep,
      ...s.groups.flatMap((gr) => gr.tabs).slice(0, 20).map((id) => ({ label: s.tabs[id].title, checked: id === t?.id, run: () => focusTab(id) })),
    ] },
    { title: "Help", items: [it("About Hawser", "about")] },
  ];
}

export function terminalContextMenu(): MenuItem[] {
  const t = activeTab();
  const c = activeController();
  const up = t?.info.state === "connected";
  return [
    it("Copy", "copy", { disabled: !c?.hasSelection() }), it("Paste", "paste"), it("Copy and Paste", "copyPaste", { disabled: !c?.hasSelection() }),
    it("Select All", "selectAll"), it("Find…", "find"), sep,
    it("Clear Scrollback", "clearScrollback"), it("Clear Screen and Scrollback", "clearAll"), sep,
    it(up ? "Disconnect" : "Reconnect", up ? "disconnect" : "reconnect"), it("Clone Session", "clone"), it("Session Options…", "sessionOptions"),
  ];
}

export { controllers };
