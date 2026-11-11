// Mouse report encoding (X10, normal, button-event, any-event tracking).

import { MouseEncoding, MouseMode } from "./terminal";

export interface MouseEvt {
  /** 0 left, 1 middle, 2 right, 3 release/none, 64 wheel up, 65 wheel down */
  button: number;
  kind: "down" | "up" | "move" | "wheel";
  /** 0-based cell coordinates */
  col: number;
  row: number;
  /** pixel coordinates within the terminal (for SGR-pixels) */
  px: number;
  py: number;
  shift: boolean;
  alt: boolean;
  ctrl: boolean;
}

/** Returns the report to send to the host, or null if the mode ignores this event. */
export function encodeMouse(e: MouseEvt, mode: MouseMode, enc: MouseEncoding): string | null {
  if (mode === MouseMode.None) return null;
  if (mode === MouseMode.X10 && e.kind !== "down") return null;
  if (e.kind === "move") {
    if (mode === MouseMode.Normal) return null;
    if (mode === MouseMode.ButtonEvent && e.button === 3) return null;
  }
  let cb = e.button;
  if (e.kind === "up" && enc !== MouseEncoding.Sgr && enc !== MouseEncoding.SgrPixels) cb = 3;
  if (e.kind === "move") cb += 32;
  if (mode !== MouseMode.X10) {
    if (e.shift) cb += 4;
    if (e.alt) cb += 8;
    if (e.ctrl) cb += 16;
  }
  const x = e.col + 1;
  const y = e.row + 1;
  switch (enc) {
    case MouseEncoding.Sgr:
      return `\x1b[<${cb};${x};${y}${e.kind === "up" ? "m" : "M"}`;
    case MouseEncoding.SgrPixels:
      return `\x1b[<${cb};${Math.max(1, Math.round(e.px))};${Math.max(1, Math.round(e.py))}${e.kind === "up" ? "m" : "M"}`;
    case MouseEncoding.Urxvt:
      return `\x1b[${cb + 32};${x};${y}M`;
    case MouseEncoding.Utf8: {
      const enc1 = (v: number) => String.fromCodePoint(v + 32);
      if (x > 2015 || y > 2015) return null;
      return "\x1b[M" + enc1(cb) + enc1(x) + enc1(y);
    }
    default:
      // classic encoding cannot address beyond column/row 223
      if (x > 223 || y > 223) return null;
      return "\x1b[M" + String.fromCharCode(cb + 32) + String.fromCharCode(x + 32) + String.fromCharCode(y + 32);
  }
}
