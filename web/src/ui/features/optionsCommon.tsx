// Shared helpers for the options dialogs.
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { ReactNode } from "react";
import { Button, Check, Field, Num, Select, Text } from "../kit";
import { pickPath } from "../filePicker";

export const isObj = (v: any) => v && typeof v === "object" && !Array.isArray(v);
export const clone = <T,>(v: T): T => (v === undefined ? v : JSON.parse(JSON.stringify(v)));

export function deepMerge(a: any, b: any): any {
  if (!isObj(a) || !isObj(b)) return b === undefined ? clone(a) : clone(b);
  const out: any = clone(a);
  for (const k of Object.keys(b)) out[k] = isObj(out[k]) && isObj(b[k]) ? deepMerge(out[k], b[k]) : clone(b[k]);
  return out;
}

/** Deep difference of eff from def (arrays and scalars compare by JSON). */
export function deepDiff(eff: any, def: any): any {
  const out: any = {};
  for (const k of Object.keys(eff ?? {})) {
    const e = eff[k], d = def?.[k];
    if (isObj(e) && isObj(d)) {
      const sub = deepDiff(e, d);
      if (Object.keys(sub).length) out[k] = sub;
    } else if (JSON.stringify(e) !== JSON.stringify(d)) out[k] = e;
  }
  return out;
}

export function getPath(o: any, p: string): any {
  return p.split(".").reduce((a, k) => (a == null ? undefined : a[k]), o);
}
export function setPath(o: any, p: string, v: any): any {
  const ks = p.split(".");
  const root = clone(o) ?? {};
  let cur = root;
  for (const k of ks.slice(0, -1)) cur = cur[k] = isObj(cur[k]) ? cur[k] : {};
  cur[ks[ks.length - 1]] = v;
  return root;
}

/** Binds form controls to a path inside an object held in React state. */
export interface Bind { get: (p: string) => any; set: (p: string, v: any) => void }

export const BText = (b: Bind, label: string, p: string, o: { hint?: string; placeholder?: string; type?: string; mono?: boolean; disabled?: boolean } = {}) =>
  <Field label={label} hint={o.hint}><Text value={b.get(p) ?? ""} onChange={(v) => b.set(p, v)} placeholder={o.placeholder} type={o.type} mono={o.mono} disabled={o.disabled} /></Field>;
export const BNum = (b: Bind, label: string, p: string, o: { hint?: string; min?: number; max?: number; step?: number; width?: number } = {}) =>
  <Field label={label} hint={o.hint}><Num value={b.get(p) ?? 0} onChange={(v) => b.set(p, v)} min={o.min} max={o.max} step={o.step} width={o.width} /></Field>;
export const BCheck = (b: Bind, label: string, p: string, hint?: string) =>
  <div><Check checked={!!b.get(p)} onChange={(v) => b.set(p, v)} label={label} />{hint && <div className="hint-block" style={{ marginTop: -4, marginLeft: 22 }}>{hint}</div>}</div>;
export const BSelect = (b: Bind, label: string, p: string, options: (readonly [string, string])[], hint?: string) =>
  <Field label={label} hint={hint}><Select value={b.get(p) ?? ""} onChange={(v) => b.set(p, v)} options={options} /></Field>;

export function Pick(p: { value: string; onChange: (v: string) => void; mode?: "open" | "folder" | "save"; placeholder?: string; label?: string }) {
  return (
    <>
      <Text value={p.value ?? ""} onChange={p.onChange} placeholder={p.placeholder} mono />
      <Button small onClick={async () => { const r = await pickPath({ mode: p.mode ?? "open", startDir: p.value || undefined }); if (r) p.onChange(r); }}>Browse…</Button>
    </>
  );
}
export const BPick = (b: Bind, label: string, p: string, mode: "open" | "folder" = "open", hint?: string) =>
  <Field label={label} hint={hint}><Pick value={b.get(p) ?? ""} onChange={(v) => b.set(p, v)} mode={mode} /></Field>;

export function ColorInput(p: { value: string; onChange: (v: string) => void; label: string }) {
  const v = /^#[0-9a-f]{6}$/i.test(p.value) ? p.value : "#000000";
  return <input type="color" className="opt-color" aria-label={p.label} title={p.label} value={v} onChange={(e) => p.onChange(e.target.value)} />;
}

/** Ordered list of strings (one input per row). */
export function StringList(p: { value: string[]; onChange: (v: string[]) => void; label: string; file?: boolean; placeholder?: string }) {
  const v = p.value ?? [];
  const upd = (i: number, s: string) => p.onChange(v.map((x, j) => (j === i ? s : x)));
  return (
    <div className="opt-list">
      {v.map((s, i) => (
        <div className="row" key={i}>
          <Text value={s} onChange={(x) => upd(i, x)} mono placeholder={p.placeholder} />
          {p.file && <Button small onClick={async () => { const r = await pickPath({ mode: "open" }); if (r) upd(i, r); }}>Browse…</Button>}
          <Button small onClick={() => p.onChange(v.filter((_, j) => j !== i))} title={`Remove ${p.label}`}>Remove</Button>
        </div>
      ))}
      <div><Button small onClick={() => p.onChange([...v, ""])}><Plus size={12} /> Add {p.label}</Button></div>
    </div>
  );
}

/** key = value map editor. */
export function KVList(p: { value: Record<string, string>; onChange: (v: Record<string, string>) => void; label: string }) {
  const entries = Object.entries(p.value ?? {});
  const write = (e: [string, string][]) => p.onChange(Object.fromEntries(e));
  return (
    <div className="opt-list">
      {entries.map(([k, v], i) => (
        <div className="row" key={i}>
          <Text value={k} onChange={(x) => write(entries.map((e, j) => (j === i ? [x, e[1]] : e)))} placeholder="Name" mono width={180} />
          <Text value={v} onChange={(x) => write(entries.map((e, j) => (j === i ? [e[0], x] : e)))} placeholder="Value" mono />
          <Button small onClick={() => write(entries.filter((_, j) => j !== i))}>Remove</Button>
        </div>
      ))}
      <div><Button small onClick={() => { let n = "NAME", c = 1; while (p.value?.[n] !== undefined) n = "NAME" + ++c; write([...entries, [n, ""]]); }}><Plus size={12} /> Add {p.label}</Button></div>
    </div>
  );
}

export function MoveButtons(p: { i: number; n: number; move: (i: number, d: number) => void }) {
  return (
    <>
      <Button small disabled={p.i === 0} title="Move up" onClick={() => p.move(p.i, -1)}><ArrowUp size={12} /></Button>
      <Button small disabled={p.i === p.n - 1} title="Move down" onClick={() => p.move(p.i, 1)}><ArrowDown size={12} /></Button>
    </>
  );
}
export const DelIcon = <Trash2 size={12} />;

export function moveItem<T>(a: T[], i: number, d: number): T[] {
  const j = i + d;
  if (j < 0 || j >= a.length) return a;
  const c = a.slice();
  [c[i], c[j]] = [c[j], c[i]];
  return c;
}

export function Section(p: { children: ReactNode }) { return <div className="section-title">{p.children}</div>; }

import { useEffect } from "react";
import { comboName, keyInfo } from "../../term/keyboard";

/** While `active`, the next non-modifier key combination is reported to onCombo (Escape cancels). */
export function useComboCapture(active: boolean, onCombo: (combo: string | null) => void) {
  useEffect(() => {
    if (!active) return;
    const h = (e: KeyboardEvent) => {
      if (["Control", "Alt", "Shift", "Meta", "AltGraph"].includes(e.key)) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      if (e.key === "Escape" && !e.ctrlKey && !e.altKey && !e.shiftKey && !e.metaKey) return onCombo(null);
      onCombo(comboName(keyInfo(e)));
    };
    window.addEventListener("keydown", h, true);
    return () => window.removeEventListener("keydown", h, true);
  }, [active, onCombo]);
}
