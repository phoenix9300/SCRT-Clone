// Global options, keyword highlighting, button bar and keyboard shortcut editors.
import { useCallback, useState } from "react";
import { COLOR_SCHEMES, ColorScheme } from "../../term/palette";
import { controllers, getApp, setGlobal, showDialog, useApp, Global, ButtonDef } from "../../state/store";
import { Keyword } from "../../view/controller";
import { cmd, DEFAULT_SHORTCUTS } from "../commands";
import { features } from "../features";
import { promptText } from "../dialogs";
import { Button, Check, Dialog, Field, Num, Select, Text } from "../kit";
import { clone, ColorInput, moveItem, MoveButtons, Pick, Section, useComboCapture } from "./optionsCommon";
import "./options.css";

const OkCancel = (close: () => void, ok: () => void, label = "OK") => <><Button onClick={close}>Cancel</Button><Button primary onClick={ok}>{label}</Button></>;

// ------------------------------------------------------------ global options

features.globalOptions = () => { showDialog<void>((close) => <GlobalOptions close={() => close()} />); };

function GlobalOptions({ close }: { close: () => void }) {
  const [g, setG] = useState<Global>(() => clone(getApp().global));
  const u = (k: string, v: any) => setG((o) => ({ ...o, [k]: v }));
  const ok = () => {
    const keys = ["theme", "fontFamily", "fontSize", "colorScheme", "showSessionManager", "showToolbar", "showStatusBar", "showButtonBar", "showCommandWindow", "confirmClose", "confirmExit", "downloadDir", "pythonPath"];
    setGlobal(Object.fromEntries(keys.map((k) => [k, g[k]])) as any);
    close();
  };
  const custom: ColorScheme[] = getApp().global.customSchemes ?? [];
  const chk = (k: string, label: string) => <Check checked={!!g[k]} onChange={(v) => u(k, v)} label={label} />;
  return (
    <Dialog title="Global Options" width={620} onClose={close} onSubmit={ok} footer={OkCancel(close, ok)}>
      <div className="opt-dialog">
        <Section>Appearance</Section>
        <Field label="Theme"><Select value={g.theme} onChange={(v) => u("theme", v)} options={[["dark", "Dark"], ["light", "Light"], ["system", "Follow system"]]} /></Field>
        <Field label="Terminal font"><Text value={g.fontFamily} onChange={(v) => u("fontFamily", v)} placeholder="default monospace" /></Field>
        <Field label="Font size"><Num value={g.fontSize} min={6} max={72} step={0.5} onChange={(v) => u("fontSize", v)} /></Field>
        <Field label="Colour scheme"><Select value={g.colorScheme} onChange={(v) => u("colorScheme", v)}
          options={[...COLOR_SCHEMES, ...custom].map((s) => [s.id, s.name] as const)} />
          <Button small onClick={() => features.customSchemes()}>Edit schemes…</Button></Field>
        <Section>Window</Section>
        {chk("showSessionManager", "Session manager")}{chk("showToolbar", "Toolbar")}{chk("showStatusBar", "Status bar")}
        {chk("showButtonBar", "Button bar")}{chk("showCommandWindow", "Command window")}
        <Section>Confirmation</Section>
        {chk("confirmClose", "Confirm before closing a connected tab")}{chk("confirmExit", "Confirm before exiting")}
        <Section>Files and scripting</Section>
        <Field label="Download folder"><Pick value={g.downloadDir} onChange={(v) => u("downloadDir", v)} mode="folder" /></Field>
        <Field label="Python path" hint="Interpreter used for Python scripts; empty = auto-detect."><Pick value={g.pythonPath} onChange={(v) => u("pythonPath", v)} /></Field>
        <Section>Editors</Section>
        <div className="row" style={{ flexWrap: "wrap" }}>
          <Button onClick={() => features.keywords()}>Keyword highlighting…</Button>
          <Button onClick={() => features.buttonBarEditor()}>Button bar…</Button>
          <Button onClick={() => features.shortcuts()}>Keyboard shortcuts…</Button>
        </div>
      </div>
    </Dialog>
  );
}

// ------------------------------------------------------- custom colour schemes

const BLANK_ANSI = ["#000000", "#cc3333", "#33aa33", "#ccaa33", "#3366cc", "#aa33aa", "#33aaaa", "#cccccc", "#555555", "#ff5555", "#55ff55", "#ffff55", "#5599ff", "#ff55ff", "#55ffff", "#ffffff"];
const ANSI_NAMES = ["Black", "Red", "Green", "Yellow", "Blue", "Magenta", "Cyan", "White"];

features.customSchemes = () => { showDialog<void>((close) => <SchemeEditor close={() => close()} />); };

function SchemeEditor({ close }: { close: () => void }) {
  const [list, setList] = useState<ColorScheme[]>(() => clone(getApp().global.customSchemes ?? []));
  const [sel, setSel] = useState(0);
  const cur = list[sel];
  const upd = (patch: Partial<ColorScheme>) => setList((l) => l.map((s, i) => (i === sel ? { ...s, ...patch } : s)));
  const nextId = () => { let n = 1; while (list.some((s) => s.id === "custom-" + n)) n++; return "custom-" + n; };
  const add = (base?: ColorScheme) => {
    const s: ColorScheme = base ? { ...clone(base), id: nextId(), name: base.name + " copy" }
      : { id: nextId(), name: "New scheme", dark: true, foreground: "#d0d0d0", background: "#101010", cursor: "#ffffff", selection: "#334466", ansi: [...BLANK_ANSI] };
    setList((l) => [...l, s]);
    setSel(list.length);
  };
  const ok = () => {
    const ids = new Set(list.map((s) => s.id));
    const cs = getApp().global.colorScheme;
    setGlobal({ customSchemes: list, ...(cs.startsWith("custom-") && !ids.has(cs) ? { colorScheme: "hawser-dark" } : {}) });
    close();
  };
  const color = (label: string, v: string, f: (x: string) => void) => (
    <label><ColorInput label={label} value={v} onChange={f} />{label}</label>
  );
  return (
    <Dialog title="Colour Schemes" width={720} height={560} onClose={close} footer={OkCancel(close, ok)}>
      <div className="opt-body opt-dialog"><div className="opt-split">
        <div className="opt-side">
          <ul aria-label="Custom schemes">{list.map((s, i) => <li key={s.id} className={i === sel ? "sel" : ""} tabIndex={0} onClick={() => setSel(i)} onKeyDown={(e) => e.key === "Enter" && (e.stopPropagation(), setSel(i))}>{s.name}</li>)}</ul>
          <div className="row" style={{ flexWrap: "wrap" }}>
            <Button small onClick={() => add()}>Add</Button>
            <Button small disabled={!cur} onClick={() => cur && add(cur)}>Duplicate</Button>
            <Button small disabled={!cur} onClick={() => { setList((l) => l.filter((_, i) => i !== sel)); setSel(0); }}>Delete</Button>
          </div>
          <Button small onClick={() => add(COLOR_SCHEMES.find((s) => s.id === getApp().global.colorScheme) ?? COLOR_SCHEMES[0])}>Copy current scheme</Button>
        </div>
        <div className="opt-main">
          {!cur ? <div className="dim">Add a scheme to start editing.</div> : <>
            <Field label="Name"><Text value={cur.name} onChange={(v) => upd({ name: v })} /></Field>
            <Check checked={cur.dark} onChange={(v) => upd({ dark: v })} label="Dark scheme" />
            <div className="opt-colors">
              {color("Foreground", cur.foreground, (v) => upd({ foreground: v }))}
              {color("Background", cur.background, (v) => upd({ background: v }))}
              {color("Cursor", cur.cursor, (v) => upd({ cursor: v }))}
              {color("Selection", cur.selection, (v) => upd({ selection: v }))}
            </div>
            <Section>ANSI colours</Section>
            <div className="opt-colors">
              {cur.ansi.map((c, i) => color((i > 7 ? "Bright " : "") + ANSI_NAMES[i % 8], c, (v) => upd({ ansi: cur.ansi.map((x, j) => (j === i ? v : x)) })))}
            </div>
          </>}
        </div>
      </div></div>
    </Dialog>
  );
}

// ---------------------------------------------------------------- keywords

features.keywords = () => { showDialog<void>((close) => <KeywordEditor close={() => close()} />); };

function KeywordEditor({ close }: { close: () => void }) {
  const [sets, setSets] = useState<Record<string, Keyword[]>>(() => clone(getApp().global.keywordSets ?? {}));
  const [sel, setSel] = useState<string>(() => Object.keys(getApp().global.keywordSets ?? {})[0] ?? "");
  const rows = sets[sel] ?? [];
  const setRows = (r: Keyword[]) => setSets((s) => ({ ...s, [sel]: r }));
  const upd = (i: number, patch: Partial<Keyword>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const add = async () => {
    const n = (await promptText("New Keyword Set", "Name of the set:"))?.trim();
    if (!n || sets[n]) return;
    setSets((s) => ({ ...s, [n]: [] }));
    setSel(n);
  };
  const rename = async () => {
    const n = (await promptText("Rename Keyword Set", "New name:", sel))?.trim();
    if (!n || n === sel || sets[n]) return;
    setSets((s) => Object.fromEntries(Object.entries(s).map(([k, v]) => [k === sel ? n : k, v])));
    setSel(n);
  };
  const del = () => {
    const rest = Object.keys(sets).filter((k) => k !== sel);
    setSets((s) => { const c = { ...s }; delete c[sel]; return c; });
    setSel(rest[0] ?? "");
  };
  const ok = () => {
    setGlobal({ keywordSets: sets });
    for (const c of controllers.values()) c.setKeywords();
    close();
  };
  return (
    <Dialog title="Keyword Highlighting" width={860} height={560} onClose={close} footer={OkCancel(close, ok)}>
      <div className="opt-body opt-dialog"><div className="opt-split">
        <div className="opt-side">
          <ul aria-label="Keyword sets">{Object.keys(sets).map((k) => <li key={k} className={k === sel ? "sel" : ""} tabIndex={0} onClick={() => setSel(k)} onKeyDown={(e) => e.key === "Enter" && (e.stopPropagation(), setSel(k))}>{k}</li>)}</ul>
          <div className="row" style={{ flexWrap: "wrap" }}>
            <Button small onClick={add}>Add</Button><Button small disabled={!sel} onClick={rename}>Rename</Button><Button small disabled={!sel} onClick={del}>Delete</Button>
          </div>
        </div>
        <div className="opt-main">
          {!sel ? <div className="dim">Create a set to add highlighting rules.</div> : <>
            <div className="opt-grid-scroll"><table className="grid">
              <thead><tr><th>Pattern</th><th>Regex</th><th>Case</th><th>Text</th><th>Background</th><th>Bold</th><th>Underl.</th><th></th></tr></thead>
              <tbody>
                {rows.map((r, i) => (
                  <tr key={i}>
                    <td><Text value={r.pattern} onChange={(v) => upd(i, { pattern: v })} mono width={190} placeholder="text or regex" /></td>
                    <td><input type="checkbox" aria-label="Regular expression" checked={!!r.regex} onChange={(e) => upd(i, { regex: e.target.checked })} /></td>
                    <td><input type="checkbox" aria-label="Case sensitive" checked={!!r.caseSensitive} onChange={(e) => upd(i, { caseSensitive: e.target.checked })} /></td>
                    <td><div className="row"><ColorInput label="Text colour" value={r.fg ?? ""} onChange={(v) => upd(i, { fg: v })} /><Button small disabled={!r.fg} onClick={() => upd(i, { fg: undefined })}>None</Button></div></td>
                    <td><div className="row"><ColorInput label="Background colour" value={r.bg ?? ""} onChange={(v) => upd(i, { bg: v })} /><Button small disabled={!r.bg} onClick={() => upd(i, { bg: undefined })}>None</Button></div></td>
                    <td><input type="checkbox" aria-label="Bold" checked={!!r.bold} onChange={(e) => upd(i, { bold: e.target.checked })} /></td>
                    <td><input type="checkbox" aria-label="Underline" checked={!!r.underline} onChange={(e) => upd(i, { underline: e.target.checked })} /></td>
                    <td><div className="row"><MoveButtons i={i} n={rows.length} move={(a, d) => setRows(moveItem(rows, a, d))} /><Button small onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove</Button></div></td>
                  </tr>
                ))}
              </tbody>
            </table></div>
            <div style={{ marginTop: 8 }}><Button small onClick={() => setRows([...rows, { pattern: "", regex: false }])}>Add rule</Button></div>
          </>}
        </div>
      </div></div>
    </Dialog>
  );
}

// --------------------------------------------------------------- button bar

features.buttonBarEditor = () => { showDialog<void>((close) => <ButtonBarEditor close={() => close()} />); };

function ButtonBarEditor({ close }: { close: () => void }) {
  const [bars, setBars] = useState<Record<string, ButtonDef[]>>(() => clone(getApp().global.buttonBars ?? { Default: [] }));
  const [active, setActive] = useState(() => getApp().global.activeButtonBar);
  const [sel, setSel] = useState(() => (active in (getApp().global.buttonBars ?? {}) ? active : Object.keys(getApp().global.buttonBars ?? {})[0] ?? ""));
  const rows = bars[sel] ?? [];
  const setRows = (r: ButtonDef[]) => setBars((s) => ({ ...s, [sel]: r }));
  const upd = (i: number, patch: Partial<ButtonDef>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const add = async () => {
    const n = (await promptText("New Button Bar", "Name of the bar:"))?.trim();
    if (!n || bars[n]) return;
    setBars((s) => ({ ...s, [n]: [] }));
    setSel(n);
  };
  const rename = async () => {
    const n = (await promptText("Rename Button Bar", "New name:", sel))?.trim();
    if (!n || n === sel || bars[n]) return;
    setBars((s) => Object.fromEntries(Object.entries(s).map(([k, v]) => [k === sel ? n : k, v])));
    if (active === sel) setActive(n);
    setSel(n);
  };
  const del = () => {
    const rest = Object.keys(bars).filter((k) => k !== sel);
    if (!rest.length) return;
    setBars((s) => { const c = { ...s }; delete c[sel]; return c; });
    if (active === sel) setActive(rest[0]);
    setSel(rest[0]);
  };
  const ok = () => { setGlobal({ buttonBars: bars, activeButtonBar: active in bars ? active : Object.keys(bars)[0] }); close(); };
  return (
    <Dialog title="Button Bar" width={860} height={560} onClose={close} footer={OkCancel(close, ok)}>
      <div className="opt-body opt-dialog"><div className="opt-split">
        <div className="opt-side">
          <ul aria-label="Button bars">{Object.keys(bars).map((k) => <li key={k} className={k === sel ? "sel" : ""} tabIndex={0} onClick={() => setSel(k)} onKeyDown={(e) => e.key === "Enter" && (e.stopPropagation(), setSel(k))}>{k}{k === active ? " (shown)" : ""}</li>)}</ul>
          <div className="row" style={{ flexWrap: "wrap" }}>
            <Button small onClick={add}>Add</Button><Button small disabled={!sel} onClick={rename}>Rename</Button><Button small disabled={Object.keys(bars).length < 2} onClick={del}>Delete</Button>
          </div>
          <Button small disabled={!sel || active === sel} onClick={() => setActive(sel)}>Show this bar</Button>
        </div>
        <div className="opt-main">
          <div className="opt-grid-scroll"><table className="grid">
            <thead><tr><th>Label</th><th>Action</th><th>Value</th><th>Colour</th><th></th></tr></thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={i}>
                  <td><Text value={r.label} onChange={(v) => upd(i, { label: v })} width={100} placeholder="Label" /></td>
                  <td><Select value={r.action} width={90} onChange={(v) => upd(i, { action: v as ButtonDef["action"], value: "" })} options={[["send", "Send"], ["script", "Script"], ["menu", "Command"]]} /></td>
                  <td>
                    {r.action === "menu" ? <Select value={r.value} onChange={(v) => upd(i, { value: v })} options={[["", "Choose…"], ...Object.keys(cmd).sort().map((k) => [k, k] as const)]} width={170} />
                      : r.action === "script" ? <div className="row"><Text value={r.value} onChange={(v) => upd(i, { value: v })} mono width={150} /><Pick value={r.value} onChange={(v) => upd(i, { value: v })} /></div>
                      : <Text value={r.value} onChange={(v) => upd(i, { value: v })} mono width={190} placeholder="text, \r = Enter" />}
                  </td>
                  <td><div className="row"><ColorInput label="Button colour" value={r.color ?? ""} onChange={(v) => upd(i, { color: v })} /><Button small disabled={!r.color} onClick={() => upd(i, { color: undefined })}>None</Button></div></td>
                  <td><div className="row"><MoveButtons i={i} n={rows.length} move={(a, d) => setRows(moveItem(rows, a, d))} /><Button small onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove</Button></div></td>
                </tr>
              ))}
            </tbody>
          </table></div>
          <div style={{ marginTop: 8 }}><Button small onClick={() => setRows([...rows, { label: "Button", action: "send", value: "" }])}>Add button</Button></div>
        </div>
      </div></div>
    </Dialog>
  );
}

// ---------------------------------------------------------------- shortcuts

features.shortcuts = () => { showDialog<void>((close) => <ShortcutEditor close={() => close()} />); };

function bindings(cmdId: string, user: Record<string, string>): string[] {
  const out = Object.entries(user).filter(([, c]) => c === cmdId).map(([k]) => k);
  for (const [k, c] of Object.entries(DEFAULT_SHORTCUTS)) if (c === cmdId && user[k] === undefined) out.push(k);
  return out;
}

function ShortcutEditor({ close }: { close: () => void }) {
  const [user, setUser] = useState<Record<string, string>>(() => ({ ...(getApp().global.shortcuts ?? {}) }));
  const [cap, setCap] = useState<string | null>(null); // command id, "@new" or "send:<combo>"
  const [pendingText, setPendingText] = useState("");
  const unbind = (u: Record<string, string>, id: string) => {
    const n = { ...u };
    for (const [k, c] of Object.entries(n)) if (c === id) delete n[k];
    for (const [k, c] of Object.entries(DEFAULT_SHORTCUTS)) if (c === id) n[k] = "none";
    return n;
  };
  const onCombo = useCallback((combo: string | null) => {
    const target = cap;
    setCap(null);
    if (!combo || !target) return;
    setUser((u) => {
      if (target === "@new") return { ...u, [combo]: "send:" + pendingText };
      if (target.startsWith("@send:")) {
        const old = target.slice(6), n = { ...u };
        const txt = n[old]; delete n[old]; n[combo] = txt; return n;
      }
      const n = unbind(u, target);
      if (DEFAULT_SHORTCUTS[combo] === target) delete n[combo]; else n[combo] = target;
      for (const [k, c] of Object.entries(DEFAULT_SHORTCUTS)) if (c === target && k !== combo && n[k] === undefined) n[k] = "none";
      return n;
    });
  }, [cap, pendingText]);
  useComboCapture(cap !== null, onCombo);
  const custom = Object.entries(user).filter(([, v]) => v.startsWith("send:"));
  const ok = () => { setGlobal({ shortcuts: user }); close(); };
  return (
    <Dialog title="Keyboard Shortcuts" width={640} height={600} onClose={close}
      footer={<><Button onClick={() => setUser({})}>Reset to Defaults</Button><span className="spacer" />{OkCancel(close, ok)}</>}>
      <div className="opt-body opt-dialog">
        <div className="hint-block" role="status">{cap ? "Press the new key combination (Escape cancels)…" : "Select Set to capture a new combination for a command."}</div>
        <div className="opt-grid-scroll"><table className="grid">
          <thead><tr><th>Command</th><th>Shortcut</th><th></th></tr></thead>
          <tbody>
            {Object.keys(cmd).sort().map((id) => {
              const keys = bindings(id, user);
              return (
                <tr key={id} className={cap === id ? "sel" : ""}>
                  <td>{id}</td>
                  <td className="mono">{cap === id ? "Press keys…" : keys.join(", ") || "-"}</td>
                  <td><div className="row">
                    <Button small onClick={() => setCap(cap === id ? null : id)}>Set</Button>
                    <Button small disabled={!keys.length} onClick={() => setUser((u) => unbind(u, id))}>Clear</Button>
                  </div></td>
                </tr>
              );
            })}
          </tbody>
        </table></div>
        <Section>Send text shortcuts</Section>
        <div className="opt-list">
          {custom.map(([k, v]) => (
            <div className="row" key={k}>
              <Button small onClick={() => setCap(cap === "@send:" + k ? null : "@send:" + k)}>{cap === "@send:" + k ? "Press keys…" : k}</Button>
              <Text mono value={v.slice(5)} onChange={(t) => setUser((u) => ({ ...u, [k]: "send:" + t }))} placeholder="Text, \r = Enter" />
              <Button small onClick={() => setUser((u) => { const n = { ...u }; delete n[k]; return n; })}>Remove</Button>
            </div>
          ))}
          <div className="row">
            <Text mono value={pendingText} onChange={setPendingText} placeholder="Text to send, e.g. ls -l\r" />
            <Button small disabled={!pendingText} onClick={() => setCap("@new")}>{cap === "@new" ? "Press keys…" : "Add shortcut…"}</Button>
          </div>
        </div>
      </div>
    </Dialog>
  );
}
