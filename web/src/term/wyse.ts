// Wyse 50 / Wyse 60 native-mode control language. It shares the screen model
// with the ANSI emulation and takes over control and escape handling.

import { BG_DIM, CELL_SIZE, EMPTY_CONTENT, FG_BLINK, FG_INVERSE, FG_INVISIBLE, FG_UNDERLINE, WIDTH_1 } from "./cell";
import type { DialectHandler, Terminal } from "./terminal";

// ESC H <c> line-drawing characters ('0'..'?')
const GRAPHICS = "┬└┌┐├┘│█┼┤─▒═┴║░";

export class WyseDialect implements DialectHandler {
  private graphics = false;
  private protectDim = false;

  constructor(private readonly t: Terminal, private readonly model: 50 | 60) {
    t.parser.vt52 = true; // dispatch every ESC <char> immediately
  }

  print(data: Uint32Array, start: number, end: number): boolean {
    if (!this.graphics) return false;
    const out = new Uint32Array(end - start);
    for (let i = start; i < end; i++) {
      const c = data[i];
      out[i - start] = c >= 0x30 && c <= 0x3f ? GRAPHICS.codePointAt(c - 0x30)! : c;
    }
    this.graphics = false;
    this.t.print(out, 0, out.length);
    this.graphics = true;
    return true;
  }

  execute(code: number): boolean {
    const t = this.t;
    switch (code) {
      case 0x08: t.cursorBackward(1); break;
      case 0x09: t.tab(1); break;
      case 0x0a: t.index(); break;
      case 0x0b: t.cursorUp(1); break;
      case 0x0c: t.cursorForward(1); break;
      case 0x0d: t.carriageReturn(); break;
      case 0x1a: this.clear(); break;
      case 0x1e: t.cursorTo(0, 0); break;
      case 0x1f: t.nextLine(); break;
      case 0x0e: t.keyboardLocked = false; break;
      case 0x0f: t.keyboardLocked = true; break;
      case 0x02: this.graphics = true; break;
      case 0x03: this.graphics = false; break;
      default: return false; // BEL, ENQ … use the common handling
    }
    return true;
  }

  private clear(): void {
    const t = this.t;
    t.eraseDisplay(2);
    t.cursorTo(0, 0);
  }

  private setAttr(code: number): void {
    const t = this.t;
    t.fg &= ~(FG_INVISIBLE | FG_BLINK | FG_INVERSE | FG_UNDERLINE);
    t.bg &= ~BG_DIM;
    if (code & 1) t.fg |= FG_INVISIBLE;
    if (code & 2) t.fg |= FG_BLINK;
    if (code & 4) t.fg |= FG_INVERSE;
    if (code & 8) t.fg |= FG_UNDERLINE;
    if ((code & 0x70) === 0x70 || this.protectDim) t.bg |= BG_DIM;
  }

  esc(_collect: number, final: number): boolean {
    const t = this.t;
    const p = t.parser;
    const buf = t.buf;
    switch (String.fromCharCode(final)) {
      case "=": p.captureNext(2, (c) => t.cursorTo(c[1] - 0x20, c[0] - 0x20)); break;
      case "a": this.readAddress(); break;
      case "?": t.onData(String.fromCharCode(buf.y + 0x20, buf.x + 0x20) + "\r"); break;
      case "b": t.onData(`${buf.y + 1}R${buf.x + 1}C`); break;
      case " ": t.onData(this.model + "\r"); break;
      case "T": case "t": t.eraseLine(0); break;
      case "Y": case "y": t.eraseDisplay(0); break;
      case "+": case "*": case ";": case ":": case ",": this.clear(); break;
      case "E": t.insertLines(1); break;
      case "R": t.deleteLines(1); break;
      case "Q": t.insertChars(1); break;
      case "W": t.deleteChars(1); break;
      case "q": t.insertMode = true; break;
      case "r": t.insertMode = false; break;
      case "I": t.backTab(1); break;
      case "i": t.tab(1); break;
      case "j": t.reverseIndex(); break;
      case "{": t.cursorTo(0, 0); break;
      case "G": p.captureNext(1, (c) => this.setAttr(c[0])); break;
      case ")": this.protectDim = true; t.bg |= BG_DIM; break;
      case "(": this.protectDim = false; t.bg &= ~BG_DIM; break;
      case "H": p.captureNext(1, (c) => {
        if (c[0] === 0x02) this.graphics = true;
        else if (c[0] === 0x03) this.graphics = false;
        else if (c[0] >= 0x30 && c[0] <= 0x3f) {
          const g = this.graphics;
          this.graphics = false;
          t.print(new Uint32Array([GRAPHICS.codePointAt(c[0] - 0x30)!]), 0, 1);
          this.graphics = g;
        }
      }); break;
      case "`": p.captureNext(1, (c) => {
        const ch = String.fromCharCode(c[0]);
        if (ch === "0") t.cursorVisible = false;
        else if (ch === "1") t.cursorVisible = true;
        else if (ch === "2" || ch === "5") (t.cursorShape = "block"), (t.cursorVisible = true);
        else if (ch === "3" || ch === "4") (t.cursorShape = "underline"), (t.cursorVisible = true);
        else if (ch === ":") t.onResizeRequest(80, t.rows);
        else if (ch === ";") t.onResizeRequest(132, t.rows);
        t.onModeChange();
      }); break;
      case "1": this.setTab(1); break;
      case "2": this.setTab(0); break;
      case "0": case "3": this.setTab(-1); break;
      case "#": t.keyboardLocked = true; break;
      case '"': t.keyboardLocked = false; break;
      case ".": p.captureNext(1, (c) => this.fill(c[0])); break;
      // sequences with one parameter byte that we accept and ignore
      case "x": case "~": case "e": case "d": case "w": case "!": case "V": case "c": case "A": case "u":
        p.captureNext(final === 0x41 ? 2 : 1, () => {});
        break;
      // message / label strings end with CR
      case "F": case "z": case "Z": case "_": this.skipUntilCR(); break;
      default: break; // unknown: ignore
    }
    return true;
  }

  private setTab(v: number): void {
    const tabs = (this.t as any).tabs as Uint8Array;
    if (v < 0) tabs.fill(0);
    else tabs[this.t.buf.x] = v;
  }

  private fill(code: number): void {
    const t = this.t;
    for (let y = 0; y < t.rows; y++) {
      const line = t.buf.line(y);
      for (let x = 0; x < t.cols; x++) {
        const o = x * CELL_SIZE;
        line.data[o] = code > 0x20 ? code | WIDTH_1 : EMPTY_CONTENT;
        line.data[o + 1] = 0;
        line.data[o + 2] = 0;
      }
    }
    t.markAllDirty();
  }

  /** ESC a <row> R <col> C with decimal numbers. */
  private readAddress(): void {
    const t = this.t;
    let row = 0;
    let col = 0;
    let stage = 0;
    const next = () => t.parser.captureNext(1, (c) => {
      const ch = c[0];
      if (ch >= 0x30 && ch <= 0x39) {
        if (stage === 0) row = row * 10 + ch - 0x30;
        else col = col * 10 + ch - 0x30;
        next();
      } else if (ch === 0x52 && stage === 0) {
        stage = 1;
        next();
      } else if (ch === 0x43) t.cursorTo(col - 1, row - 1);
    });
    next();
  }

  private skipUntilCR(): void {
    const next = () => this.t.parser.captureNext(1, (c) => {
      if (c[0] !== 0x0d) next();
    });
    next();
  }
}
