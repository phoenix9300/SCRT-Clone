// Translates keyboard events into the byte sequences each emulation expects.

import { KeyFamily } from "./emulation";

export interface KeyInfo {
  key: string; // KeyboardEvent.key
  code: string; // KeyboardEvent.code
  ctrl: boolean;
  alt: boolean;
  shift: boolean;
  meta: boolean;
}

export interface KeyState {
  family: KeyFamily;
  appCursor: boolean;
  appKeypad: boolean;
  newLineMode: boolean;
  /** Backspace sends BS (0x08) instead of DEL (0x7f) */
  backspaceIsBS: boolean;
  /** Delete sends DEL (0x7f) instead of the VT "remove" sequence */
  deleteIsDel: boolean;
  altIsMeta: boolean;
  vt52: boolean;
}

const ESC = "\x1b";
const CSI = "\x1b[";
const SS3 = "\x1bO";

const FKEY_TILDE: Record<number, number> = {
  1: 11, 2: 12, 3: 13, 4: 14, 5: 15, 6: 17, 7: 18, 8: 19, 9: 20, 10: 21, 11: 23, 12: 24,
  13: 25, 14: 26, 15: 28, 16: 29, 17: 31, 18: 32, 19: 33, 20: 34,
};

function modParam(k: KeyInfo): number {
  return 1 + (k.shift ? 1 : 0) + (k.alt ? 2 : 0) + (k.ctrl ? 4 : 0);
}

function fnNumber(key: string): number {
  const m = /^F(\d{1,2})$/.exec(key);
  return m ? parseInt(m[1], 10) : 0;
}

function functionKey(n: number, k: KeyInfo, s: KeyState): string | null {
  const mod = modParam(k);
  switch (s.family) {
    case "sco": {
      if (n > 12) return null;
      const base = "MNOPQRSTUVWX";
      const shift = "YZabcdefghij";
      const ctrl = "klmnopqrstuv";
      const both = "wxyz@[\\]^_`{";
      const set = k.ctrl && k.shift ? both : k.ctrl ? ctrl : k.shift ? shift : base;
      return CSI + set[n - 1];
    }
    case "wyse": {
      if (n > 16) return null;
      const c = String.fromCharCode((k.shift ? 0x60 : 0x40) + n - 1);
      return "\x01" + c + "\r";
    }
    case "linux":
      if (n <= 5 && mod === 1) return CSI + "[" + "ABCDE"[n - 1];
      break;
    case "vt100":
    case "vt220":
      if (s.vt52 && n <= 4) return ESC + "PQRS"[n - 1];
      // Shift+F1..F10 reach F11..F20 on keyboards without those keys
      if (k.shift && n <= 10 && s.family === "vt220") return CSI + FKEY_TILDE[n + 10] + "~";
      if (n <= 4) return SS3 + "PQRS"[n - 1];
      return FKEY_TILDE[n] ? CSI + FKEY_TILDE[n] + "~" : null;
  }
  // xterm (and linux F6+)
  if (n <= 4 && s.family !== "linux") {
    return mod === 1 ? SS3 + "PQRS"[n - 1] : `${CSI}1;${mod}${"PQRS"[n - 1]}`;
  }
  const code = FKEY_TILDE[n];
  if (!code) return null;
  return mod === 1 ? `${CSI}${code}~` : `${CSI}${code};${mod}~`;
}

function cursorKey(final: string, k: KeyInfo, s: KeyState): string {
  if (s.family === "wyse") {
    return { A: "\x0b", B: "\x0a", C: "\x0c", D: "\x08", H: "\x1e", F: ESC + "T" }[final] ?? "";
  }
  if (s.vt52) return ESC + final;
  const mod = modParam(k);
  if (mod !== 1 && s.family === "xterm") return `${CSI}1;${mod}${final}`;
  return (s.appCursor ? SS3 : CSI) + final;
}

function editKey(n: number, k: KeyInfo, s: KeyState): string {
  const mod = modParam(k);
  if (mod !== 1 && s.family === "xterm") return `${CSI}${n};${mod}~`;
  return `${CSI}${n}~`;
}

const KEYPAD_APP: Record<string, string> = {
  Numpad0: "p", Numpad1: "q", Numpad2: "r", Numpad3: "s", Numpad4: "t", Numpad5: "u", Numpad6: "v",
  Numpad7: "w", Numpad8: "x", Numpad9: "y", NumpadDecimal: "n", NumpadEnter: "M", NumpadSubtract: "m",
  NumpadAdd: "l", NumpadDivide: "Q", NumpadMultiply: "R",
};

/**
 * Returns the characters to send for a key press, or null when the key is not
 * handled here (plain printable input arrives through the text input path).
 */
export function encodeKey(k: KeyInfo, s: KeyState): string | null {
  const key = k.key;

  // application keypad
  if (s.appKeypad && k.code.startsWith("Numpad") && KEYPAD_APP[k.code] && s.family !== "sco" && s.family !== "wyse") {
    if (/^[0-9.]$/.test(key) || k.code === "NumpadEnter" || !/^Numpad\d$/.test(k.code)) {
      return (s.vt52 ? ESC + "?" : SS3) + KEYPAD_APP[k.code];
    }
  }

  const fn = fnNumber(key);
  if (fn) return functionKey(fn, k, s);

  const altPrefix = k.alt && s.altIsMeta ? ESC : "";

  switch (key) {
    case "ArrowUp":
      return cursorKey("A", k, s);
    case "ArrowDown":
      return cursorKey("B", k, s);
    case "ArrowRight":
      return cursorKey("C", k, s);
    case "ArrowLeft":
      return cursorKey("D", k, s);
    case "Home":
      if (s.family === "xterm" || s.family === "sco" || s.family === "wyse") return cursorKey("H", k, s);
      return editKey(1, k, s);
    case "End":
      if (s.family === "xterm" || s.family === "sco" || s.family === "wyse") return cursorKey("F", k, s);
      return editKey(4, k, s);
    case "Insert":
      if (s.family === "sco") return CSI + "L";
      if (s.family === "wyse") return ESC + "q";
      return editKey(2, k, s);
    case "Delete":
      if (s.deleteIsDel || s.family === "sco") return "\x7f";
      if (s.family === "wyse") return ESC + "W";
      return editKey(3, k, s);
    case "PageUp":
      if (s.family === "sco") return CSI + "I";
      if (s.family === "wyse") return ESC + "J";
      return editKey(5, k, s);
    case "PageDown":
      if (s.family === "sco") return CSI + "G";
      if (s.family === "wyse") return ESC + "K";
      return editKey(6, k, s);
    case "Enter":
      if (k.code === "NumpadEnter" && s.appKeypad) return SS3 + "M";
      return altPrefix + (s.newLineMode ? "\r\n" : "\r");
    case "Tab":
      if (k.shift) return s.family === "wyse" ? ESC + "I" : CSI + "Z";
      return altPrefix + "\t";
    case "Backspace": {
      let bs = s.backspaceIsBS || s.family === "sco" || s.family === "wyse";
      if (k.ctrl) bs = !bs;
      return altPrefix + (bs ? "\x08" : "\x7f");
    }
    case "Escape":
      return altPrefix + ESC;
  }

  // Control combinations
  if (k.ctrl && !k.meta) {
    let c: string | null = null;
    if (key.length === 1) {
      const u = key.toUpperCase();
      const code = u.charCodeAt(0);
      if (code >= 0x40 && code <= 0x5f) c = String.fromCharCode(code & 0x1f);
      else if (key === " " || key === "2" || key === "`") c = "\x00";
      else if (key === "3") c = ESC;
      else if (key === "4") c = "\x1c";
      else if (key === "5") c = "\x1d";
      else if (key === "6") c = "\x1e";
      else if (key === "7" || key === "/" || key === "-") c = "\x1f";
      else if (key === "8" || key === "?") c = "\x7f";
    }
    if (c === null && /^Key[A-Z]$/.test(k.code)) {
      // non-latin layouts: fall back to the physical key
      c = String.fromCharCode(k.code.charCodeAt(3) & 0x1f);
    }
    if (c !== null) return altPrefix + c;
    return null;
  }

  // Alt/Meta + printable = ESC prefix
  if (k.alt && s.altIsMeta && !k.meta && key.length === 1) return ESC + key;

  return null;
}

/** Human readable name of a key combination, e.g. "Ctrl+Shift+F5". */
export function comboName(k: KeyInfo): string {
  const parts: string[] = [];
  if (k.ctrl) parts.push("Ctrl");
  if (k.alt) parts.push("Alt");
  if (k.shift) parts.push("Shift");
  if (k.meta) parts.push("Meta");
  let name = k.key;
  if (name === " ") name = "Space";
  else if (name.length === 1) name = name.toUpperCase();
  else if (name.startsWith("Arrow")) name = name.slice(5);
  if (/^Key[A-Z]$/.test(k.code) && (k.ctrl || k.alt)) name = k.code.slice(3);
  else if (/^Digit\d$/.test(k.code) && (k.ctrl || k.alt)) name = k.code.slice(5);
  if (["Control", "Alt", "Shift", "Meta"].includes(k.key)) return parts.join("+");
  parts.push(name);
  return parts.join("+");
}

export function keyInfo(e: KeyboardEvent): KeyInfo {
  return { key: e.key, code: e.code, ctrl: e.ctrlKey, alt: e.altKey, shift: e.shiftKey, meta: e.metaKey };
}
