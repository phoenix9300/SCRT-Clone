// Application state (zustand) and the actions that talk to the engine.

import { ReactNode } from "react";
import { create } from "zustand";
import { rpc } from "../rpc";
import { ColorScheme } from "../term/palette";
import { ControllerHost, Keyword, TerminalController } from "../view/controller";

export interface TreeNode {
  name: string; path: string; folder: boolean; children?: TreeNode[];
  summary?: { protocol: string; host: string; port: number; username: string; description: string; tabColor?: string };
}

export interface ConnInfo {
  id: number; session: string; title: string; state: string; message: string; failed: boolean; protocol: string; target: string;
  cols: number; rows: number; connectedAt?: number; logging: boolean; logFile?: string; recording: boolean;
  details?: Record<string, string>; options?: any;
}

export interface Tab {
  id: string; kind: "terminal" | "sftp"; connId: number; title: string; customTitle?: string; info: ConnInfo;
  activity: boolean; bell: boolean; locked: boolean;
  sftp?: { id: number; home: string };
}

export interface Group { id: string; tabs: string[]; active: string | null }

export interface ButtonDef { label: string; action: "send" | "script" | "menu"; value: string; color?: string }

export interface Global {
  theme: "dark" | "light" | "system";
  fontFamily: string; fontSize: number; colorScheme: string; customSchemes: ColorScheme[];
  showSessionManager: boolean; showToolbar: boolean; showStatusBar: boolean; showButtonBar: boolean; showCommandWindow: boolean;
  sidebarWidth: number; confirmClose: boolean; confirmExit: boolean;
  keywordSets: Record<string, Keyword[]>;
  buttonBars: Record<string, ButtonDef[]>; activeButtonBar: string;
  shortcuts: Record<string, string>; // combo -> command id
  recentHosts: string[]; downloadDir: string; pythonPath: string;
  /** session paths connected automatically when the application starts */
  autoSessions: string[];
  [k: string]: any;
}

export const GLOBAL_DEFAULTS: Global = {
  theme: "dark", fontFamily: "", fontSize: 14, colorScheme: "hawser-dark", customSchemes: [],
  showSessionManager: true, showToolbar: true, showStatusBar: true, showButtonBar: false, showCommandWindow: false,
  sidebarWidth: 250, confirmClose: true, confirmExit: true,
  keywordSets: {
    Network: [
      { pattern: "\\b(error|fail(ed|ure)?|down|denied|critical|fatal)\\b", regex: true, fg: "#ff6b6b", bold: true },
      { pattern: "\\b(warn(ing)?|timeout|retry)\\b", regex: true, fg: "#e2b55a" },
      { pattern: "\\b(up|ok|success(ful)?|connected|established)\\b", regex: true, fg: "#7fc878" },
      { pattern: "\\b\\d{1,3}(\\.\\d{1,3}){3}(/\\d{1,2})?\\b", regex: true, fg: "#5aa2f0" },
    ],
  },
  buttonBars: { Default: [] }, activeButtonBar: "Default", shortcuts: {}, recentHosts: [], downloadDir: "", pythonPath: "", autoSessions: [],
};

export interface Toast { id: number; text: string; kind: "info" | "error"; progress?: number; sticky?: boolean; key?: string }

interface State {
  phase: "boot" | "token" | "ready" | "lost";
  bootError: string;
  info: any;
  global: Global;
  tree: TreeNode | null;
  tabs: Record<string, Tab>;
  groups: Group[];
  activeGroup: string;
  split: "row" | "column";
  dialogs: { id: number; node: ReactNode }[];
  toasts: Toast[];
  findOpen: boolean;
  selectedSession: string;
}

export const useApp = create<State>(() => ({
  phase: "boot", bootError: "", info: null, global: GLOBAL_DEFAULTS, tree: null, tabs: {}, groups: [{ id: "g1", tabs: [], active: null }],
  activeGroup: "g1", split: "row", dialogs: [], toasts: [], findOpen: false, selectedSession: "",
}));

const set = useApp.setState;
const get = useApp.getState;

/** Terminal controllers by tab id (kept outside React state). */
export const controllers = new Map<string, TerminalController>();

// ---------------------------------------------------------------- helpers

let seq = 1;
export const uid = (p: string) => p + seq++;

export function activeTab(): Tab | null {
  const s = get();
  const g = s.groups.find((x) => x.id === s.activeGroup) ?? s.groups[0];
  return g?.active ? (s.tabs[g.active] ?? null) : null;
}

export function activeController(): TerminalController | null {
  const t = activeTab();
  return t ? (controllers.get(t.id) ?? null) : null;
}

export function tabByConn(connId: number): Tab | undefined {
  return Object.values(get().tabs).find((t) => t.connId === connId && t.kind === "terminal");
}

export function toast(text: string, kind: "info" | "error" = "info", opts: Partial<Toast> = {}): number {
  const id = seq++;
  set((s) => ({ toasts: [...s.toasts.filter((t) => !opts.key || t.key !== opts.key), { id, text, kind, ...opts }] }));
  if (!opts.sticky) setTimeout(() => dismissToast(id), kind === "error" ? 8000 : 4000);
  return id;
}

export function dismissToast(id: number): void {
  set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) }));
}

export function fail(e: unknown): void {
  toast(String((e as Error)?.message ?? e), "error");
}

/** Show a dialog; resolves with the value passed to close(). */
export function showDialog<T>(render: (close: (v?: T) => void) => ReactNode): Promise<T | undefined> {
  return new Promise((resolve) => {
    const id = seq++;
    const close = (v?: T) => {
      set((s) => ({ dialogs: s.dialogs.filter((d) => d.id !== id) }));
      resolve(v);
      setTimeout(() => activeController()?.focus(), 0);
    };
    set((s) => ({ dialogs: [...s.dialogs, { id, node: render(close) }] }));
  });
}

export function setGlobal(patch: Partial<Global>): void {
  set((s) => ({ global: { ...s.global, ...patch } }));
  rpc.call("global.set", patch).catch(fail);
  applyGlobal();
}

function applyGlobal(): void {
  const g = get().global;
  const theme = g.theme === "system" ? (matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark") : g.theme;
  document.documentElement.dataset.theme = theme;
  for (const c of controllers.values()) c.applyOptions();
}

export async function reloadTree(): Promise<void> {
  set({ tree: await rpc.call("sessions.tree") });
}

// ---------------------------------------------------------- controller host

/** Hooks supplied by the UI layer (commands, menus, dialogs). */
export const ui = {
  shortcut: (_combo: string, _e: KeyboardEvent, _c: TerminalController | null): boolean => false,
  contextMenu: (_e: MouseEvent, _c: TerminalController): void => {},
  confirmPaste: async (_text: string): Promise<boolean> => true,
  confirm: async (_title: string, _message: string, _ok?: string): Promise<boolean> => true,
  print: (_text: string): void => {},
  onStartup: (_actions: any[]): void => {},
};

const statusListeners = new Set<() => void>();
export function onStatus(fn: () => void): () => void {
  statusListeners.add(fn);
  return () => statusListeners.delete(fn);
}

const host: ControllerHost = {
  defaults: () => {
    const g = get().global;
    return { fontFamily: g.fontFamily, fontSize: g.fontSize, colorScheme: g.colorScheme, customSchemes: g.customSchemes ?? [] };
  },
  keywords: (name) => (name ? (get().global.keywordSets?.[name] ?? []) : []),
  shortcut: (combo, e, c) => ui.shortcut(combo, e, c),
  contextMenu: (e, c) => ui.contextMenu(e, c),
  confirmPaste: (t) => ui.confirmPaste(t),
  notify: (kind, ctrl, data) => {
    const tab = tabByConn(ctrl.connId);
    if (!tab) return;
    if (kind === "print") return ui.print(data);
    if (kind === "cursor" || kind === "size" || kind === "selection") {
      statusListeners.forEach((f) => f());
      return;
    }
    const isActive = activeTab()?.id === tab.id;
    if (kind === "title") patchTab(tab.id, { title: tab.customTitle || ctrl.title || tab.info.title });
    else if (kind === "activity" && !isActive && !tab.activity) patchTab(tab.id, { activity: true });
    else if (kind === "bell") {
      if (!isActive) patchTab(tab.id, { bell: true });
      const mode = ctrl.opts.terminal.bell;
      if (mode === "audible" || mode === "both") beep();
      if (mode === "visual" || mode === "both") flash(tab.id);
    }
  },
};

let audio: AudioContext | null = null;
function beep(): void {
  try {
    audio ??= new AudioContext();
    const o = audio.createOscillator();
    const g = audio.createGain();
    o.frequency.value = 880;
    g.gain.value = 0.08;
    o.connect(g).connect(audio.destination);
    o.start();
    o.stop(audio.currentTime + 0.12);
  } catch {
    /* audio unavailable */
  }
}

function flash(tabId: string): void {
  const el = document.querySelector<HTMLElement>(`[data-view="${tabId}"] .term-root`);
  if (!el) return;
  el.animate([{ filter: "brightness(1.8)" }, { filter: "none" }], { duration: 160 });
}

export function patchTab(id: string, patch: Partial<Tab>): void {
  set((s) => (s.tabs[id] ? { tabs: { ...s.tabs, [id]: { ...s.tabs[id], ...patch } } } : {}));
}

// --------------------------------------------------------------- tabs

function addTab(tab: Tab, groupId?: string): void {
  set((s) => {
    const gid = groupId ?? s.activeGroup;
    return {
      tabs: { ...s.tabs, [tab.id]: tab },
      groups: s.groups.map((g) => (g.id === gid ? { ...g, tabs: [...g.tabs, tab.id], active: tab.id } : g)),
      activeGroup: gid,
    };
  });
}

function adopt(info: ConnInfo): Tab {
  const tab: Tab = { id: uid("t"), kind: "terminal", connId: info.id, title: info.title, info, activity: false, bell: false, locked: false };
  const ctrl = new TerminalController(info.id, info.options, host);
  ctrl.connected = info.state === "connected";
  controllers.set(tab.id, ctrl);
  addTab(tab);
  return tab;
}

export interface ConnectRequest {
  session?: string; overrides?: any; password?: string; title?: string; cloneOf?: number;
}

export async function connect(req: ConnectRequest): Promise<Tab | null> {
  try {
    const info: ConnInfo = await rpc.call("conn.open", { ...req, cols: 80, rows: 24 });
    const tab = adopt(info);
    // state changes may have been broadcast before the tab existed
    rpc.call<ConnInfo>("conn.info", { id: info.id }).then(onConnState).catch(() => {});
    const host = req.overrides?.host;
    if (host && !req.session) {
      const label = (req.overrides.username ? req.overrides.username + "@" : "") + host;
      const g = get().global;
      setGlobal({ recentHosts: [label, ...g.recentHosts.filter((h) => h !== label)].slice(0, 20) });
    }
    return tab;
  } catch (e) {
    fail(e);
    return null;
  }
}

export function focusTab(id: string): void {
  set((s) => {
    const g = s.groups.find((x) => x.tabs.includes(id));
    if (!g) return {};
    return {
      groups: s.groups.map((x) => (x.id === g.id ? { ...x, active: id } : x)),
      activeGroup: g.id,
      tabs: { ...s.tabs, [id]: { ...s.tabs[id], activity: false, bell: false } },
    };
  });
  setTimeout(() => controllers.get(id)?.focus(), 0);
  statusListeners.forEach((f) => f());
}

function removeTab(id: string): void {
  controllers.get(id)?.dispose();
  controllers.delete(id);
  set((s) => {
    const tabs = { ...s.tabs };
    delete tabs[id];
    let groups = s.groups.map((g) => {
      if (!g.tabs.includes(id)) return g;
      const i = g.tabs.indexOf(id);
      const rest = g.tabs.filter((t) => t !== id);
      return { ...g, tabs: rest, active: g.active === id ? (rest[Math.min(i, rest.length - 1)] ?? null) : g.active };
    });
    if (groups.length > 1) groups = groups.filter((g) => g.tabs.length > 0);
    if (groups.length === 0) groups = [{ id: uid("g"), tabs: [], active: null }];
    const activeGroup = groups.some((g) => g.id === s.activeGroup) ? s.activeGroup : groups[0].id;
    return { tabs, groups, activeGroup };
  });
  setTimeout(() => activeController()?.focus(), 0);
}

export async function closeTab(id: string, force = false): Promise<void> {
  const tab = get().tabs[id];
  if (!tab) return;
  if (!force && tab.kind === "terminal" && tab.info.state === "connected" && get().global.confirmClose) {
    if (!(await ui.confirm("Close session", `"${tab.title}" is connected. Disconnect and close it?`, "Close"))) return;
  }
  if (tab.kind === "sftp" && tab.sftp) rpc.call("sftp.close", { id: tab.sftp.id }).catch(() => {});
  else rpc.call("conn.close", { id: tab.connId }).catch(() => {});
  removeTab(id);
}

export function moveTab(id: string, toGroup: string, before?: string): void {
  set((s) => {
    let groups = s.groups.map((g) => ({ ...g, tabs: g.tabs.filter((t) => t !== id) }));
    groups = groups.map((g) => {
      if (g.id !== toGroup) return g.active === id ? { ...g, active: g.tabs[0] ?? null } : g;
      const tabs = [...g.tabs];
      const i = before ? tabs.indexOf(before) : -1;
      tabs.splice(i < 0 ? tabs.length : i, 0, id);
      return { ...g, tabs, active: id };
    });
    if (groups.length > 1) groups = groups.filter((g) => g.tabs.length > 0);
    return { groups, activeGroup: groups.some((g) => g.id === toGroup) ? toGroup : groups[0].id };
  });
  setTimeout(() => controllers.get(id)?.focus(), 0);
}

/** Move the active tab into a new tab group beside (row) or below (column). */
export function splitActive(dir: "row" | "column"): void {
  const t = activeTab();
  if (!t) return;
  const gid = uid("g");
  set((s) => ({ split: dir, groups: [...s.groups, { id: gid, tabs: [], active: null }] }));
  moveTab(t.id, gid);
}

export function cycleTab(delta: number): void {
  const s = get();
  const g = s.groups.find((x) => x.id === s.activeGroup);
  if (!g || !g.tabs.length) return;
  const i = g.tabs.indexOf(g.active ?? "");
  focusTab(g.tabs[(i + delta + g.tabs.length) % g.tabs.length]);
}

// ------------------------------------------------------------------ boot

function onConnState(info: ConnInfo): void {
  const tab = tabByConn(info.id);
  if (!tab) return;
  const prev = tab.info;
  const ctrl = controllers.get(tab.id);
  patchTab(tab.id, { info: { ...prev, ...info, options: info.options ?? prev.options } });
  if (ctrl) {
    ctrl.connected = info.state === "connected";
    if (info.state !== prev.state || info.message !== prev.message) {
      if (info.state === "connecting") ctrl.status(info.message || "Connecting ...");
      else if (info.state === "connected" && prev.state !== "connected") ctrl.status("Connected to " + info.target, "ok");
      else if (info.state === "disconnected") {
        ctrl.status(info.message || "Disconnected", info.failed ? "error" : "info");
        if (info.options?.connection?.closeOnDisconnect && !info.failed) closeTab(tab.id, true);
      }
    }
  }
  statusListeners.forEach((f) => f());
}

export async function refreshConn(connId: number): Promise<void> {
  const tab = tabByConn(connId);
  if (!tab) return;
  const info: ConnInfo = await rpc.call("conn.info", { id: connId });
  patchTab(tab.id, { info });
  controllers.get(tab.id)?.applyOptions(info.options);
}

export async function boot(): Promise<void> {
  rpc.init();
  rpc.onStatus = (ok) => {
    if (!ok && get().phase === "ready") set({ phase: "lost" });
  };
  try {
    await rpc.connect();
  } catch (e) {
    set({ phase: "token", bootError: rpc.token ? String((e as Error).message) : "" });
    return;
  }
  const [info, global, tree, conns] = await Promise.all([
    rpc.call("app.info"), rpc.call("global.get"), rpc.call("sessions.tree"), rpc.call<ConnInfo[]>("conn.list"),
  ]);
  set({ info, global: { ...GLOBAL_DEFAULTS, ...global }, tree });
  applyGlobal();

  rpc.on("conn.state", onConnState);
  // a tab opened by the engine (scripts: ConnectInTab, Clone)
  rpc.on("conn.opened", (info: ConnInfo) => {
    if (!tabByConn(info.id)) {
      adopt(info);
      rpc.call<ConnInfo>("conn.info", { id: info.id }).then(onConnState).catch(() => {});
    }
  });
  rpc.on("conn.closed", (d) => {
    const tab = tabByConn(d.id);
    if (tab) removeTab(tab.id);
  });
  rpc.on("conn.notify", (d) => {
    const tab = tabByConn(d.id);
    const c = tab && controllers.get(tab.id);
    if (c && d.event === "remoteEcho") c.forcedEcho = !d.value;
  });
  rpc.on("sessions.changed", () => reloadTree().catch(() => {}));
  rpc.on("global.changed", (g) => {
    set({ global: { ...GLOBAL_DEFAULTS, ...g } });
    applyGlobal();
  });
  rpc.on("app.startup", (a) => ui.onStartup(a ?? []));

  set({ phase: "ready" });
  // re-attach to sessions that survived a window reload
  for (const c of conns) {
    const tab = adopt(c);
    await rpc.call("conn.attach", { id: c.id }).catch(() => {});
    if (c.state !== "connected") controllers.get(tab.id)?.status(c.message || "Disconnected", c.failed ? "error" : "info");
  }
  if (info.startup?.length) ui.onStartup(info.startup);
  else if (!conns.length) {
    // sessions marked "Open at Startup" (only for a fresh start, not a window reload)
    for (const path of get().global.autoSessions ?? []) void connect({ session: path });
  }
}

export function addSftpTab(sftp: { id: number; title: string; home: string; session: string }): void {
  const info: ConnInfo = { id: 0, session: sftp.session, title: sftp.title, state: "connected", message: "", failed: false, protocol: "sftp", target: sftp.title, cols: 0, rows: 0, logging: false, recording: false };
  addTab({ id: uid("t"), kind: "sftp", connId: 0, title: "SFTP: " + sftp.title, info, activity: false, bell: false, locked: false, sftp: { id: sftp.id, home: sftp.home } });
}

export { set as setApp, get as getApp };
