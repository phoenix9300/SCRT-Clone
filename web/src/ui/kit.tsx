// Small UI building blocks shared by every dialog and panel.
import { X } from "lucide-react";
import React, { ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

export function Dialog(p: {
  title: string; width?: number; height?: number; onClose: () => void; children: ReactNode; footer?: ReactNode;
  /** called on Enter outside multi-line inputs */
  onSubmit?: () => void; flush?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const prev = document.activeElement as HTMLElement | null;
    const first = el.querySelector<HTMLElement>("[autofocus], input:not([type=checkbox]), select, textarea, button.primary");
    (first ?? el).focus();
    return () => prev?.focus?.();
  }, []);
  const onKey = (e: React.KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === "Escape") p.onClose();
    else if (e.key === "Enter" && p.onSubmit && !(e.target instanceof HTMLTextAreaElement) && !(e.target instanceof HTMLButtonElement)) {
      e.preventDefault();
      p.onSubmit();
    } else if (e.key === "Tab") {
      // keep focus inside the dialog
      const f = Array.from(ref.current!.querySelectorAll<HTMLElement>("button, input, select, textarea, [tabindex]")).filter((n) => !n.hasAttribute("disabled") && n.tabIndex >= 0);
      if (!f.length) return;
      const i = f.indexOf(document.activeElement as HTMLElement);
      if (e.shiftKey && i <= 0) (e.preventDefault(), f[f.length - 1].focus());
      else if (!e.shiftKey && i === f.length - 1) (e.preventDefault(), f[0].focus());
    }
  };
  return (
    <div className="scrim" onMouseDown={(e) => e.target === e.currentTarget && p.onClose()}>
      <div className="dialog" ref={ref} tabIndex={-1} role="dialog" aria-label={p.title} onKeyDown={onKey}
        style={{ width: p.width ?? 440, height: p.height }}>
        <header><span>{p.title}</span><button className="tb-btn" onClick={p.onClose} aria-label="Close"><X size={16} /></button></header>
        {p.flush ? p.children : <div className="content">{p.children}</div>}
        {p.footer && <footer>{p.footer}</footer>}
      </div>
    </div>
  );
}

export function Field(p: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label>{p.label}</label>
      <div className="row">{p.children}</div>
      {p.hint && <div className="hint">{p.hint}</div>}
    </div>
  );
}

export function Text(p: { value: string; onChange: (v: string) => void; placeholder?: string; type?: string; width?: number | string; autoFocus?: boolean; disabled?: boolean; mono?: boolean }) {
  return <input className={"input" + (p.mono ? " mono" : "")} type={p.type ?? "text"} value={p.value ?? ""} placeholder={p.placeholder} autoFocus={p.autoFocus} disabled={p.disabled}
    spellCheck={false} autoComplete="off" style={{ width: p.width ?? "100%" }} onChange={(e) => p.onChange(e.target.value)} />;
}

export function Num(p: { value: number; onChange: (v: number) => void; min?: number; max?: number; width?: number; disabled?: boolean; step?: number }) {
  return <input className="input" type="number" value={Number.isFinite(p.value) ? p.value : 0} min={p.min} max={p.max} step={p.step} disabled={p.disabled}
    style={{ width: p.width ?? 90 }} onChange={(e) => p.onChange(e.target.value === "" ? 0 : Number(e.target.value))} />;
}

export function Select<T extends string>(p: { value: T; onChange: (v: T) => void; options: (readonly [T, string])[] | { id: string; label: string }[]; width?: number | string; disabled?: boolean }) {
  const opts = (p.options as any[]).map((o) => (Array.isArray(o) ? o : [o.id, o.label]));
  return (
    <select className="input" value={p.value} disabled={p.disabled} style={{ width: p.width ?? "100%" }} onChange={(e) => p.onChange(e.target.value as T)}>
      {opts.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
    </select>
  );
}

export function Check(p: { checked: boolean; onChange: (v: boolean) => void; label: ReactNode; disabled?: boolean }) {
  return (
    <label className="check">
      <input type="checkbox" checked={!!p.checked} disabled={p.disabled} onChange={(e) => p.onChange(e.target.checked)} />
      <span>{p.label}</span>
    </label>
  );
}

export function Button(p: { children: ReactNode; onClick?: () => void; primary?: boolean; danger?: boolean; small?: boolean; disabled?: boolean; title?: string }) {
  return <button className={"btn" + (p.primary ? " primary" : "") + (p.danger ? " danger" : "") + (p.small ? " small" : "")} disabled={p.disabled} title={p.title} onClick={p.onClick}>{p.children}</button>;
}

export interface MenuItem {
  label?: string; keys?: string; checked?: boolean; disabled?: boolean; sep?: boolean; run?: () => void; children?: MenuItem[];
}

/** A popup menu positioned at (x, y); closes on outside click or Escape. */
export function PopupMenu(p: { x: number; y: number; items: MenuItem[]; onClose: () => void; onNavigate?: (dir: -1 | 1) => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ x: p.x, y: p.y });
  const [active, setActive] = useState(-1);
  const [sub, setSub] = useState<{ i: number; x: number; y: number } | null>(null);
  useLayoutEffect(() => {
    const el = ref.current!;
    const r = el.getBoundingClientRect();
    setPos({ x: Math.max(4, Math.min(p.x, innerWidth - r.width - 4)), y: Math.max(4, Math.min(p.y, innerHeight - r.height - 4)) });
    el.focus();
  }, [p.x, p.y]);
  useEffect(() => {
    const down = (e: MouseEvent) => {
      if (!(e.target as HTMLElement).closest(".popup, .menu-title")) p.onClose();
    };
    window.addEventListener("mousedown", down, true);
    return () => window.removeEventListener("mousedown", down, true);
  }, []);
  const usable = p.items.map((it, i) => (!it.sep && !it.disabled ? i : -1)).filter((i) => i >= 0);
  const choose = (it: MenuItem, i: number, el?: HTMLElement) => {
    if (it.disabled || it.sep) return;
    if (it.children) {
      const r = (el ?? ref.current!.children[i] as HTMLElement).getBoundingClientRect();
      setSub({ i, x: r.right - 2, y: r.top - 4 });
      return;
    }
    p.onClose();
    it.run?.();
  };
  const onKey = (e: React.KeyboardEvent) => {
    e.stopPropagation();
    e.preventDefault();
    const idx = usable.indexOf(active);
    if (e.key === "Escape") p.onClose();
    else if (e.key === "ArrowDown") setActive(usable[(idx + 1) % usable.length]);
    else if (e.key === "ArrowUp") setActive(usable[(idx - 1 + usable.length) % usable.length]);
    else if (e.key === "Enter" && active >= 0) choose(p.items[active], active);
    else if (e.key === "ArrowRight") p.items[active]?.children ? choose(p.items[active], active) : p.onNavigate?.(1);
    else if (e.key === "ArrowLeft") p.onNavigate?.(-1);
    else if (e.key.length === 1) {
      const i = p.items.findIndex((it) => !it.sep && !it.disabled && it.label?.toLowerCase().startsWith(e.key.toLowerCase()));
      if (i >= 0) setActive(i);
    }
  };
  return createPortal(
    <>
      <div className="popup" ref={ref} tabIndex={-1} style={{ left: pos.x, top: pos.y }} onKeyDown={onKey} onContextMenu={(e) => e.preventDefault()}>
        {p.items.map((it, i) => it.sep ? <div className="sep" key={i} /> : (
          <div key={i} className={"item" + (it.disabled ? " disabled" : "") + (i === active ? " active" : "")}
            onMouseEnter={(e) => { setActive(i); if (it.children && !it.disabled) choose(it, i, e.currentTarget); else setSub(null); }}
            onClick={(e) => choose(it, i, e.currentTarget)}>
            <span className="check">{it.checked ? "✓" : ""}</span>
            <span className="label">{it.label}</span>
            <span className="keys">{it.children ? "▸" : it.keys}</span>
          </div>
        ))}
      </div>
      {sub && p.items[sub.i]?.children && <PopupMenu x={sub.x} y={sub.y} items={p.items[sub.i].children!} onClose={p.onClose} />}
    </>,
    document.body,
  );
}
