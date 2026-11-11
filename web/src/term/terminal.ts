// The terminal emulator core: interprets host output and maintains the
// screen model. It has no DOM dependencies so it can run in tests and workers.

import { Buffer, SavedCursor } from "./buffer";
import {
  BG_DIM, BG_EXT, BG_ITALIC, BG_OVERLINE, BG_PROTECTED, CELL_SIZE, CM_DEFAULT, CM_PALETTE, CM_RGB,
  CODE_MASK, COLOR_BITS, COMBINED, EMPTY_CONTENT, ExtAttr, FG_BLINK, FG_BOLD, FG_INVERSE, FG_INVISIBLE,
  FG_STRIKE, FG_UNDERLINE, Line, LineAttr, UnderlineStyle, WIDTH_1, WIDTH_2, WIDTH_MASK, WIDTH_SHIFT,
} from "./cell";
import { Charset, DEC_SPECIAL, Decoder, charsetFor, cp437, createDecoder } from "./charsets";
import { Emulation, EmulationId, emulationById } from "./emulation";
import { Params, Parser, ParserHandler } from "./parser";
import { wcwidth } from "./wcwidth";

export interface TerminalOptions {
  cols: number;
  rows: number;
  scrollback: number;
  emulation: EmulationId;
  encoding: string;
  /** reply to ENQ (0x05) */
  answerback: string;
  /** LF also performs CR ("new line mode") */
  newLineMode: boolean;
  autoWrap: boolean;
  /** honour colour SGR sequences */
  ansiColor: boolean;
  /** allow the host to switch between 80 and 132 columns / resize the window */
  allowResize: boolean;
  allowTitleChange: boolean;
  /** allow OSC 52 clipboard writes */
  allowClipboardWrite: boolean;
  /** interpret 8-bit C1 controls */
  c1Controls: boolean;
  tabWidth: number;
}

export const DEFAULT_OPTIONS: TerminalOptions = {
  cols: 80,
  rows: 24,
  scrollback: 5000,
  emulation: "xterm",
  encoding: "utf-8",
  answerback: "",
  newLineMode: false,
  autoWrap: true,
  ansiColor: true,
  allowResize: true,
  allowTitleChange: true,
  allowClipboardWrite: true,
  c1Controls: false,
  tabWidth: 8,
};

export type CursorShape = "block" | "underline" | "bar";

export const enum MouseMode {
  None = 0,
  X10 = 9,
  Normal = 1000,
  ButtonEvent = 1002,
  AnyEvent = 1003,
}

export const enum MouseEncoding {
  Default = 0,
  Utf8 = 1005,
  Sgr = 1006,
  Urxvt = 1015,
  SgrPixels = 1016,
}

export interface Hyperlink {
  uri: string;
  id: string;
}

/** Colours the host can query / override with OSC sequences. */
export interface DynamicColors {
  /** palette overrides (index -> 0xRRGGBB) */
  palette: Map<number, number>;
  foreground: number | null;
  background: number | null;
  cursor: number | null;
}

const ESC = "\x1b";
const CSI = "\x1b[";

export class Terminal implements ParserHandler {
  cols: number;
  rows: number;
  readonly options: TerminalOptions;
  emulation: Emulation;

  normal: Buffer;
  alt: Buffer;
  buf: Buffer;

  readonly parser: Parser;
  private decoder: Decoder;
  private decodeBuf = new Uint32Array(4096);

  // ---- current rendition
  fg = 0;
  bg = 0;
  ext: ExtAttr | null = null;

  // ---- character sets
  private charsets: Charset[] = [null, null, null, null];
  private gl = 0;
  private gr = 2;
  private singleShift = -1;
  private altFont = 0; // SGR 10/11/12 (SCO / Linux console)

  // ---- modes
  insertMode = false;
  newLineMode = false;
  /** SRM reset = local echo on */
  localEcho = false;
  keyboardLocked = false;
  appCursorKeys = false;
  appKeypad = false;
  originMode = false;
  autoWrap = true;
  reverseWrap = false;
  reverseVideo = false;
  cursorVisible = true;
  cursorBlink = true;
  cursorShape: CursorShape | null = null; // null = use the user's preference
  bracketedPaste = false;
  mouseMode: MouseMode = MouseMode.None;
  mouseEncoding: MouseEncoding = MouseEncoding.Default;
  focusEvents = false;
  altScroll = true;
  leftRightMarginMode = false;
  backarrowSendsBS: boolean | null = null; // DECBKM: null = user preference
  syncOutput = false;
  allow132 = true;
  metaSendsEscape = true;
  modifyOtherKeys = 0;
  vt52 = false;
  private protectedMode = false;
  wrapNext = false;

  private tabs: Uint8Array;
  private lastPrinted = 0;
  private savedModes = new Map<number, boolean>();

  title = "";
  iconName = "";
  private titleStack: string[] = [];
  cwd = "";

  readonly links: Hyperlink[] = [{ uri: "", id: "" }];
  readonly colors: DynamicColors = { palette: new Map(), foreground: null, background: null, cursor: null };

  // ---- printer
  private printerMode = 0; // 0 off, 1 controller (pass-through), 2 auto print
  private printBuf: string[] = [];
  private printTail = "";

  // ---- dirty tracking (screen rows)
  dirtyTop = 0;
  dirtyBottom: number;
  /** incremented whenever the scrollback grows/shrinks or the viewport must be recomputed */
  scrollGeneration = 0;

  // ---- events
  onData: (data: string) => void = () => {};
  onTitle: (title: string) => void = () => {};
  onBell: () => void = () => {};
  onResizeRequest: (cols: number, rows: number) => void = () => {};
  onClipboard: (text: string) => void = () => {};
  onPrint: (text: string) => void = () => {};
  onModeChange: () => void = () => {};
  onColorChange: () => void = () => {};
  /** resolve a colour for OSC queries: index 0-255, or "fg" | "bg" | "cursor" */
  colorQuery: (which: number | "fg" | "bg" | "cursor") => number = () => 0;
  /** secondary dialect handler (Wyse); when set it receives print/execute/esc */
  dialectHandler: DialectHandler | null = null;

  constructor(opts: Partial<TerminalOptions> = {}) {
    this.options = { ...DEFAULT_OPTIONS, ...opts };
    this.cols = Math.max(2, this.options.cols);
    this.rows = Math.max(1, this.options.rows);
    this.emulation = emulationById(this.options.emulation);
    this.normal = new Buffer(this.cols, this.rows, this.options.scrollback);
    this.alt = new Buffer(this.cols, this.rows, 0);
    this.buf = this.normal;
    this.parser = new Parser(this);
    this.decoder = createDecoder(this.options.encoding);
    this.tabs = new Uint8Array(this.cols);
    this.dirtyBottom = this.rows - 1;
    this.applyOptions();
    this.resetTabs();
  }

  private applyOptions(): void {
    this.newLineMode = this.options.newLineMode;
    this.autoWrap = this.options.autoWrap;
    this.parser.c1 = this.options.c1Controls;
    this.parser.linuxOsc = this.emulation.id === "linux";
  }

  setEmulation(id: EmulationId): void {
    this.options.emulation = id;
    this.emulation = emulationById(id);
    this.parser.linuxOsc = id === "linux";
    this.onModeChange();
  }

  setEncoding(encoding: string): void {
    this.options.encoding = encoding;
    this.decoder = createDecoder(encoding);
  }

  setScrollback(n: number): void {
    this.options.scrollback = n;
    this.normal.maxScrollback = n;
    this.normal.trim(true);
    this.scrollGeneration++;
    this.markAllDirty();
  }

  get isAltScreen(): boolean {
    return this.buf === this.alt;
  }

  // ------------------------------------------------------------------ input

  /** Feed raw bytes received from the host. */
  write(data: Uint8Array): void {
    let off = 0;
    while (off < data.length) {
      const chunk = data.subarray(off, off + 4000);
      off += chunk.length;
      if (this.decodeBuf.length < chunk.length + 8) this.decodeBuf = new Uint32Array(chunk.length + 8);
      const n = this.decoder.decode(chunk, this.decodeBuf);
      if (n > 0) this.parser.parse(this.decodeBuf, n);
    }
  }

  /** Feed already-decoded text (used by tests and local messages). */
  writeString(s: string): void {
    const out = new Uint32Array(s.length);
    let n = 0;
    for (const ch of s) out[n++] = ch.codePointAt(0)!;
    this.parser.parse(out, n);
  }

  private send(s: string): void {
    this.onData(s);
  }

  // --------------------------------------------------------------- dirtiness

  markDirty(y1: number, y2 = y1): void {
    if (y1 < this.dirtyTop) this.dirtyTop = y1;
    if (y2 > this.dirtyBottom) this.dirtyBottom = y2;
  }

  markAllDirty(): void {
    this.dirtyTop = 0;
    this.dirtyBottom = this.rows - 1;
  }

  /** Returns the dirty row range ([top, bottom], bottom < top when clean) and resets it. */
  consumeDirty(): [number, number] {
    const r: [number, number] = [Math.max(0, this.dirtyTop), Math.min(this.rows - 1, this.dirtyBottom)];
    this.dirtyTop = this.rows;
    this.dirtyBottom = -1;
    return r;
  }

  // ------------------------------------------------------------------ resize

  resize(cols: number, rows: number): void {
    cols = Math.max(2, Math.floor(cols));
    rows = Math.max(1, Math.floor(rows));
    if (cols === this.cols && rows === this.rows) return;
    this.normal.resize(cols, rows, true);
    this.alt.resize(cols, rows, false);
    const oldCols = this.cols;
    this.cols = cols;
    this.rows = rows;
    const tabs = new Uint8Array(cols);
    tabs.set(this.tabs.subarray(0, Math.min(oldCols, cols)));
    for (let i = oldCols; i < cols; i++) if (i % this.options.tabWidth === 0) tabs[i] = 1;
    this.tabs = tabs;
    this.wrapNext = false;
    this.scrollGeneration++;
    this.markAllDirty();
  }

  private resetTabs(): void {
    this.tabs.fill(0);
    for (let i = this.options.tabWidth; i < this.cols; i += this.options.tabWidth) this.tabs[i] = 1;
  }

  // ------------------------------------------------------------------- print

  print(data: Uint32Array, start: number, end: number): void {
    if (this.dialectHandler && this.dialectHandler.print(data, start, end)) return;
    if (this.printerMode === 1) {
      for (let i = start; i < end; i++) this.printBuf.push(String.fromCodePoint(data[i]));
      return;
    }
    const buf = this.buf;
    const cols = this.cols;
    let line = buf.line(buf.y);
    let d = line.data;
    const fg = this.fg;
    const bg = this.bg & ~BG_EXT;
    const ext = this.ext;
    let cs = this.charsets[this.gl];
    const altFont = this.altFont;
    let dirtyY = buf.y;

    for (let i = start; i < end; i++) {
      let code = data[i];
      if (code < 0x7f) {
        if (this.singleShift >= 0) {
          const ss = this.charsets[this.singleShift];
          this.singleShift = -1;
          if (ss) {
            const m = ss[code];
            if (m !== undefined) code = m;
          }
        } else if (cs) {
          const m = cs[code];
          if (m !== undefined) code = m;
        } else if (altFont === 12) {
          code = cp437(code | 0x80);
        }
      } else if (code >= 0xa0 && code < 0x100 && this.gr !== 2 && this.charsets[this.gr]) {
        // 8-bit GR invoking a 94-set (rare; used with NRCS on 8-bit links)
        const m = this.charsets[this.gr]![code & 0x7f];
        if (m !== undefined) code = m;
      }

      const w = code < 0x300 ? 1 : wcwidth(code);

      if (w === 0 || (code > 0x2000 && this.prevEndsWithZwj(line, buf.x))) {
        let px = this.wrapNext ? buf.x : buf.x - 1;
        if (px >= 0) {
          if ((d[px * CELL_SIZE] & WIDTH_MASK) === 0 && px > 0) px--;
          line.addCombining(px, code);
          this.markDirty(buf.y);
        }
        continue;
      }

      if (this.wrapNext) {
        this.wrapNext = false;
        if (this.autoWrap) {
          this.markDirty(Math.min(dirtyY, buf.y), Math.max(dirtyY, buf.y));
          this.wrapLine();
          line = buf.line(buf.y);
          d = line.data;
          dirtyY = buf.y;
        }
      }

      const right = buf.x > buf.marginRight ? cols - 1 : buf.marginRight;
      if (w === 2 && buf.x >= right) {
        if (!this.autoWrap || right < 1) continue;
        line.fill(buf.x, buf.x + 1, 0, bg);
        this.markDirty(Math.min(dirtyY, buf.y), Math.max(dirtyY, buf.y));
        this.wrapLine();
        line = buf.line(buf.y);
        d = line.data;
        dirtyY = buf.y;
      }

      let x = buf.x;
      if (this.insertMode) line.insertCells(x, w, right, bg);

      const o = x * CELL_SIZE;
      // repair wide characters we are about to cut in half
      const old = d[o];
      if ((old & WIDTH_MASK) === 0 && x > 0) {
        if (d[o - CELL_SIZE] & COMBINED) line.combined?.delete(x - 1);
        d[o - CELL_SIZE] = EMPTY_CONTENT;
      } else if ((old & WIDTH_MASK) === WIDTH_2 && w === 1 && x + 1 < cols) {
        d[o + CELL_SIZE] = EMPTY_CONTENT;
      }
      if (w === 2 && x + 2 < cols && (d[o + CELL_SIZE] & WIDTH_MASK) === WIDTH_2) {
        d[o + 2 * CELL_SIZE] = EMPTY_CONTENT;
      }

      if (ext === null && (old & COMBINED) === 0 && (d[o + 2] & BG_EXT) === 0) {
        d[o] = code | (w << WIDTH_SHIFT);
        d[o + 1] = fg;
        d[o + 2] = bg;
      } else {
        line.setCell(x, code | (w << WIDTH_SHIFT), fg, bg, ext);
      }
      if (w === 2) {
        line.setCell(x + 1, 0, fg, bg, ext);
      }
      x += w;
      if (x > right) {
        x = right;
        this.wrapNext = this.autoWrap;
      }
      buf.x = x;
      this.lastPrinted = code;
    }
    // keep the SCS state consistent if a single shift was consumed
    cs = this.charsets[this.gl];
    this.markDirty(Math.min(dirtyY, buf.y), Math.max(dirtyY, buf.y));
    if (this.printerMode === 2) this.printTail = "";
  }

  private prevEndsWithZwj(line: Line, x: number): boolean {
    const px = this.wrapNext ? x : x - 1;
    if (px < 0 || !line.combined) return false;
    let p = px;
    if ((line.data[p * CELL_SIZE] & WIDTH_MASK) === 0 && p > 0) p--;
    const t = line.combined.get(p);
    return t !== undefined && t.charCodeAt(t.length - 1) === 0x200d;
  }

  /** Move to the next line because of auto-wrap. */
  private wrapLine(): void {
    const buf = this.buf;
    this.index();
    buf.x = this.leftRightMarginMode && buf.x >= buf.marginLeft ? buf.marginLeft : 0;
    buf.line(buf.y).wrapped = true;
  }

  // ----------------------------------------------------------------- execute

  execute(code: number): void {
    if (this.dialectHandler && this.dialectHandler.execute(code)) return;
    if (this.printerMode === 1) {
      if (code === 0x0a || code === 0x0d || code === 0x09 || code === 0x0c) this.printBuf.push(String.fromCharCode(code));
      return;
    }
    switch (code) {
      case 0x05:
        if (this.options.answerback) this.send(this.options.answerback);
        break;
      case 0x07:
        this.onBell();
        break;
      case 0x08:
        this.backspace();
        break;
      case 0x09:
        this.tab(1);
        break;
      case 0x0a:
      case 0x0b:
      case 0x0c:
        this.lineFeed();
        break;
      case 0x0d:
        this.carriageReturn();
        break;
      case 0x0e:
        this.gl = 1;
        break;
      case 0x0f:
        this.gl = 0;
        break;
      case 0x84:
        this.index();
        break;
      case 0x85:
        this.nextLine();
        break;
      case 0x88:
        this.tabs[this.buf.x] = 1;
        break;
      case 0x8d:
        this.reverseIndex();
        break;
      case 0x8e:
        this.singleShift = 2;
        break;
      case 0x8f:
        this.singleShift = 3;
        break;
    }
  }

  backspace(): void {
    const buf = this.buf;
    if (this.wrapNext) {
      this.wrapNext = false;
      if (this.emulation.id !== "xterm" && this.emulation.id !== "linux") return;
    }
    const left = this.leftRightMarginMode && buf.x >= buf.marginLeft ? buf.marginLeft : 0;
    if (buf.x > left) buf.x--;
    else if (this.reverseWrap && this.autoWrap && buf.y > 0 && buf.line(buf.y).wrapped) {
      buf.y--;
      buf.x = this.leftRightMarginMode ? buf.marginRight : this.cols - 1;
    }
  }

  tab(n: number): void {
    const buf = this.buf;
    if (this.wrapNext) return;
    const right = buf.x > buf.marginRight ? this.cols - 1 : buf.marginRight;
    let x = buf.x;
    while (n-- > 0 && x < right) {
      x++;
      while (x < right && !this.tabs[x]) x++;
    }
    buf.x = x;
  }

  backTab(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    const left = this.originMode && this.leftRightMarginMode ? buf.marginLeft : 0;
    let x = buf.x;
    while (n-- > 0 && x > left) {
      x--;
      while (x > left && !this.tabs[x]) x--;
    }
    buf.x = x;
  }

  carriageReturn(): void {
    const buf = this.buf;
    this.wrapNext = false;
    if (this.leftRightMarginMode && (buf.x >= buf.marginLeft || this.originMode)) buf.x = buf.marginLeft;
    else buf.x = 0;
  }

  lineFeed(): void {
    const buf = this.buf;
    if (this.printerMode === 2) this.autoPrintLine();
    this.index();
    if (this.newLineMode) this.carriageReturn();
    else this.wrapNext = false;
    buf.line(buf.y).wrapped = false;
  }

  /** IND: move down, scrolling at the bottom margin. */
  index(): void {
    const buf = this.buf;
    this.wrapNext = false;
    if (buf.y === buf.scrollBottom) {
      if (!this.leftRightMarginMode || (buf.x >= buf.marginLeft && buf.x <= buf.marginRight)) this.scrollUp(1);
    } else if (buf.y < this.rows - 1) {
      buf.y++;
    }
  }

  /** RI: move up, scrolling at the top margin. */
  reverseIndex(): void {
    const buf = this.buf;
    this.wrapNext = false;
    if (buf.y === buf.scrollTop) {
      if (!this.leftRightMarginMode || (buf.x >= buf.marginLeft && buf.x <= buf.marginRight)) this.scrollDown(1);
    } else if (buf.y > 0) {
      buf.y--;
    }
  }

  nextLine(): void {
    this.index();
    this.carriageReturn();
  }

  // --------------------------------------------------------------- scrolling

  private get fullWidthMargins(): boolean {
    return this.buf.marginLeft === 0 && this.buf.marginRight === this.cols - 1;
  }

  scrollUp(n: number, top = this.buf.scrollTop, bottom = this.buf.scrollBottom): void {
    const buf = this.buf;
    const height = bottom - top + 1;
    if (n > height) n = height;
    if (n <= 0) return;
    if (this.fullWidthMargins) {
      let kept = false;
      const keep = buf === this.normal;
      for (let i = 0; i < n; i++) kept = buf.scrollUp(top, bottom, this.bg, keep) || kept;
      if (kept) {
        this.scrollGeneration++;
        this.markAllDirty();
      } else this.markDirty(top, bottom);
    } else {
      const l = buf.marginLeft;
      const r = buf.marginRight + 1;
      for (let y = top; y <= bottom - n; y++) buf.line(y).copyFrom(buf.line(y + n), l, r, l);
      for (let y = bottom - n + 1; y <= bottom; y++) buf.line(y).fill(l, r, 0, this.bg);
      for (let y = top; y <= bottom; y++) {
        buf.line(y).fixWide(l);
        buf.line(y).fixWide(r - 1);
      }
      this.markDirty(top, bottom);
    }
  }

  scrollDown(n: number, top = this.buf.scrollTop, bottom = this.buf.scrollBottom): void {
    const buf = this.buf;
    const height = bottom - top + 1;
    if (n > height) n = height;
    if (n <= 0) return;
    if (this.fullWidthMargins) {
      for (let i = 0; i < n; i++) buf.scrollDown(top, bottom, this.bg);
    } else {
      const l = buf.marginLeft;
      const r = buf.marginRight + 1;
      for (let y = bottom; y >= top + n; y--) buf.line(y).copyFrom(buf.line(y - n), l, r, l);
      for (let y = top; y < top + n; y++) buf.line(y).fill(l, r, 0, this.bg);
      for (let y = top; y <= bottom; y++) {
        buf.line(y).fixWide(l);
        buf.line(y).fixWide(r - 1);
      }
    }
    this.markDirty(top, bottom);
  }

  // ------------------------------------------------------------ cursor moves

  /** Absolute cursor positioning (0-based), honouring origin mode. */
  cursorTo(x: number, y: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    if (this.originMode) {
      const left = this.leftRightMarginMode ? buf.marginLeft : 0;
      const right = this.leftRightMarginMode ? buf.marginRight : this.cols - 1;
      x += left;
      y += buf.scrollTop;
      buf.x = Math.min(Math.max(x, left), right);
      buf.y = Math.min(Math.max(y, buf.scrollTop), buf.scrollBottom);
    } else {
      buf.x = Math.min(Math.max(x, 0), this.cols - 1);
      buf.y = Math.min(Math.max(y, 0), this.rows - 1);
    }
  }

  cursorUp(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    const limit = buf.y >= buf.scrollTop ? buf.scrollTop : 0;
    buf.y = Math.max(limit, buf.y - n);
  }

  cursorDown(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    const limit = buf.y <= buf.scrollBottom ? buf.scrollBottom : this.rows - 1;
    buf.y = Math.min(limit, buf.y + n);
  }

  cursorForward(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    const limit = buf.x <= buf.marginRight ? buf.marginRight : this.cols - 1;
    buf.x = Math.min(limit, buf.x + n);
  }

  cursorBackward(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    const limit = buf.x >= buf.marginLeft ? buf.marginLeft : 0;
    buf.x = Math.max(limit, buf.x - n);
  }

  saveCursor(): void {
    const buf = this.buf;
    buf.saved = {
      x: buf.x,
      y: buf.y,
      fg: this.fg,
      bg: this.bg,
      ext: this.ext,
      originMode: this.originMode,
      wrapNext: this.wrapNext,
      gl: this.gl,
      gr: this.gr,
      charsets: this.charsets.slice(),
      protectedMode: this.protectedMode,
    };
  }

  restoreCursor(): void {
    const buf = this.buf;
    const s: SavedCursor | null = buf.saved;
    if (!s) {
      buf.x = 0;
      buf.y = 0;
      this.fg = 0;
      this.bg = 0;
      this.ext = null;
      this.originMode = false;
      this.wrapNext = false;
      this.gl = 0;
      this.charsets = [null, null, null, null];
      return;
    }
    buf.x = Math.min(s.x, this.cols - 1);
    buf.y = Math.min(s.y, this.rows - 1);
    this.fg = s.fg;
    this.bg = s.bg;
    this.ext = s.ext;
    this.originMode = s.originMode;
    this.wrapNext = s.wrapNext;
    this.gl = s.gl;
    this.gr = s.gr;
    this.charsets = s.charsets.slice();
    this.protectedMode = s.protectedMode;
  }

  // ------------------------------------------------------------------ erase

  /** ED: 0 below, 1 above, 2 all, 3 scrollback. */
  eraseDisplay(mode: number, selective = false): void {
    const buf = this.buf;
    this.wrapNext = false;
    switch (mode) {
      case 0:
        this.eraseCells(buf.y, buf.x, this.cols, selective);
        for (let y = buf.y + 1; y < this.rows; y++) this.eraseCells(y, 0, this.cols, selective, true);
        break;
      case 1:
        for (let y = 0; y < buf.y; y++) this.eraseCells(y, 0, this.cols, selective, true);
        this.eraseCells(buf.y, 0, buf.x + 1, selective);
        break;
      case 2:
        for (let y = 0; y < this.rows; y++) this.eraseCells(y, 0, this.cols, selective, true);
        break;
      case 3:
        if (buf === this.normal) {
          buf.clearScrollback();
          this.scrollGeneration++;
          this.markAllDirty();
        }
        break;
    }
  }

  /** EL: 0 right, 1 left, 2 whole line. */
  eraseLine(mode: number, selective = false): void {
    const buf = this.buf;
    this.wrapNext = false;
    switch (mode) {
      case 0:
        this.eraseCells(buf.y, buf.x, this.cols, selective);
        break;
      case 1:
        this.eraseCells(buf.y, 0, buf.x + 1, selective);
        break;
      case 2:
        this.eraseCells(buf.y, 0, this.cols, selective, true);
        break;
    }
  }

  eraseCells(y: number, start: number, end: number, selective = false, whole = false): void {
    const line = this.buf.line(y);
    if (!line) return;
    if (selective) {
      const d = line.data;
      for (let x = Math.max(0, start); x < Math.min(end, this.cols); x++) {
        if (!(d[x * CELL_SIZE + 2] & BG_PROTECTED)) line.setCell(x, EMPTY_CONTENT, 0, d[x * CELL_SIZE + 2] & COLOR_BITS, null);
      }
    } else {
      line.fill(start, end, 0, this.bg);
      if (whole) {
        line.wrapped = false;
        line.lineAttr = LineAttr.Normal;
      }
    }
    this.markDirty(y);
  }

  insertChars(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    if (buf.x < buf.marginLeft || buf.x > buf.marginRight) return;
    buf.line(buf.y).insertCells(buf.x, n, buf.marginRight, this.bg);
    this.markDirty(buf.y);
  }

  deleteChars(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    if (buf.x < buf.marginLeft || buf.x > buf.marginRight) return;
    buf.line(buf.y).deleteCells(buf.x, n, buf.marginRight, this.bg);
    this.markDirty(buf.y);
  }

  eraseChars(n: number): void {
    const buf = this.buf;
    this.wrapNext = false;
    buf.line(buf.y).fill(buf.x, buf.x + n, 0, this.bg);
    this.markDirty(buf.y);
  }

  insertLines(n: number): void {
    const buf = this.buf;
    if (buf.y < buf.scrollTop || buf.y > buf.scrollBottom) return;
    if (this.leftRightMarginMode && (buf.x < buf.marginLeft || buf.x > buf.marginRight)) return;
    this.scrollDown(n, buf.y, buf.scrollBottom);
    this.carriageReturn();
  }

  deleteLines(n: number): void {
    const buf = this.buf;
    if (buf.y < buf.scrollTop || buf.y > buf.scrollBottom) return;
    if (this.leftRightMarginMode && (buf.x < buf.marginLeft || buf.x > buf.marginRight)) return;
    const height = buf.scrollBottom - buf.y + 1;
    if (n > height) n = height;
    if (this.fullWidthMargins) {
      for (let i = 0; i < n; i++) buf.scrollUp(buf.y, buf.scrollBottom, this.bg, false);
      this.markDirty(buf.y, buf.scrollBottom);
    } else {
      this.scrollUp(n, buf.y, buf.scrollBottom);
    }
    this.carriageReturn();
  }

  /** Insert (n > 0) or delete (n < 0) columns at x within the scroll region. */
  private shiftColumns(x: number, n: number): void {
    const buf = this.buf;
    if (buf.y < buf.scrollTop || buf.y > buf.scrollBottom) return;
    if (buf.x < buf.marginLeft || buf.x > buf.marginRight) return;
    for (let y = buf.scrollTop; y <= buf.scrollBottom; y++) {
      if (n > 0) buf.line(y).insertCells(x, n, buf.marginRight, this.bg);
      else buf.line(y).deleteCells(x, -n, buf.marginRight, this.bg);
    }
    this.markDirty(buf.scrollTop, buf.scrollBottom);
  }

  // --------------------------------------------------------- screen switches

  private useAltScreen(alt: boolean, clear: boolean): void {
    const target = alt ? this.alt : this.normal;
    if (this.buf === target) return;
    target.x = this.buf.x;
    target.y = this.buf.y;
    this.buf = target;
    if (alt && clear) {
      for (let y = 0; y < this.rows; y++) this.alt.line(y).fill(0, this.cols, 0, this.bg);
      for (let y = 0; y < this.rows; y++) {
        this.alt.line(y).wrapped = false;
        this.alt.line(y).lineAttr = LineAttr.Normal;
      }
    }
    this.wrapNext = false;
    this.scrollGeneration++;
    this.markAllDirty();
    this.onModeChange();
  }

  // ------------------------------------------------------------------ resets

  softReset(): void {
    this.cursorVisible = true;
    this.insertMode = false;
    this.originMode = false;
    this.autoWrap = this.options.autoWrap;
    this.appCursorKeys = false;
    this.appKeypad = false;
    this.keyboardLocked = false;
    this.reverseWrap = false;
    this.leftRightMarginMode = false;
    this.buf.scrollTop = 0;
    this.buf.scrollBottom = this.rows - 1;
    this.buf.marginLeft = 0;
    this.buf.marginRight = this.cols - 1;
    this.fg = 0;
    this.bg = 0;
    this.ext = null;
    this.protectedMode = false;
    this.charsets = [null, null, null, null];
    this.gl = 0;
    this.gr = 2;
    this.singleShift = -1;
    this.altFont = 0;
    this.buf.saved = null;
    this.wrapNext = false;
    this.cursorShape = null;
    this.onModeChange();
  }

  /** RIS: full reset. Scrollback is kept. */
  reset(): void {
    this.useAltScreen(false, false);
    this.softReset();
    this.newLineMode = this.options.newLineMode;
    this.localEcho = false;
    this.reverseVideo = false;
    this.bracketedPaste = false;
    this.mouseMode = MouseMode.None;
    this.mouseEncoding = MouseEncoding.Default;
    this.focusEvents = false;
    this.altScroll = true;
    this.syncOutput = false;
    this.modifyOtherKeys = 0;
    this.backarrowSendsBS = null;
    this.vt52 = false;
    this.parser.vt52 = false;
    this.parser.reset();
    this.decoder.reset();
    this.printerMode = 0;
    this.printBuf = [];
    this.savedModes.clear();
    this.resetTabs();
    this.colors.palette.clear();
    this.colors.foreground = this.colors.background = this.colors.cursor = null;
    this.titleStack = [];
    for (let y = 0; y < this.rows; y++) {
      const l = this.buf.line(y);
      l.fill(0, this.cols, 0, 0);
      l.wrapped = false;
      l.lineAttr = LineAttr.Normal;
    }
    this.buf.x = 0;
    this.buf.y = 0;
    this.markAllDirty();
    this.onColorChange();
    this.onModeChange();
  }

  /** Clear the visible screen (user command), keeping the scrollback. */
  clearScreen(): void {
    for (let y = 0; y < this.rows; y++) this.eraseCells(y, 0, this.cols, false, true);
    this.buf.x = 0;
    this.buf.y = 0;
    this.wrapNext = false;
  }

  clearScrollback(): void {
    this.normal.clearScrollback();
    this.scrollGeneration++;
    this.markAllDirty();
  }

  // --------------------------------------------------------------------- ESC

  escDispatch(collect: number, final: number): void {
    if (this.dialectHandler && this.dialectHandler.esc(collect, final)) return;
    if (this.vt52) {
      this.vt52Dispatch(final);
      return;
    }
    const ch = String.fromCharCode(final);
    if (collect === 0) {
      switch (ch) {
        case "7":
          this.saveCursor();
          break;
        case "8":
          this.restoreCursor();
          break;
        case "D":
          this.index();
          break;
        case "E":
          this.nextLine();
          break;
        case "H":
          this.tabs[this.buf.x] = 1;
          break;
        case "M":
          this.reverseIndex();
          break;
        case "N":
          this.singleShift = 2;
          break;
        case "O":
          this.singleShift = 3;
          break;
        case "Z":
          this.sendDA1();
          break;
        case "c":
          this.reset();
          break;
        case "=":
          this.appKeypad = true;
          this.onModeChange();
          break;
        case ">":
          this.appKeypad = false;
          this.onModeChange();
          break;
        case "6": // DECBI
          if (this.buf.x === this.buf.marginLeft) this.shiftColumns(this.buf.x, 1);
          else this.cursorBackward(1);
          break;
        case "9": // DECFI
          if (this.buf.x === this.buf.marginRight) this.shiftColumns(this.buf.marginLeft, -1);
          else this.cursorForward(1);
          break;
        case "n":
          this.gl = 2;
          break;
        case "o":
          this.gl = 3;
          break;
        case "~":
          this.gr = 1;
          break;
        case "}":
          this.gr = 2;
          break;
        case "|":
          this.gr = 3;
          break;
        case "\\":
          break; // ST
      }
      return;
    }
    const i1 = collect > 0xff ? collect >> 8 : collect;
    switch (String.fromCharCode(i1)) {
      case "(":
        this.charsets[0] = charsetFor(ch);
        break;
      case ")":
        this.charsets[1] = charsetFor(ch);
        break;
      case "*":
        this.charsets[2] = charsetFor(ch);
        break;
      case "+":
        this.charsets[3] = charsetFor(ch);
        break;
      case "-":
      case ".":
      case "/":
        // 96-character sets: only Latin-1 style sets exist, treat as ASCII
        this.charsets[i1 - 0x2c] = null;
        break;
      case "#":
        this.lineAttrDispatch(ch);
        break;
      case " ":
        if (ch === "F") this.parser.c1 = this.options.c1Controls;
        else if (ch === "G") this.parser.c1 = true;
        break;
      case "%":
        break; // character set selection (UTF-8 / default): transport decides
    }
  }

  private lineAttrDispatch(ch: string): void {
    const buf = this.buf;
    const line = buf.line(buf.y);
    switch (ch) {
      case "3":
        line.lineAttr = LineAttr.DoubleHeightTop;
        break;
      case "4":
        line.lineAttr = LineAttr.DoubleHeightBottom;
        break;
      case "5":
        line.lineAttr = LineAttr.Normal;
        break;
      case "6":
        line.lineAttr = LineAttr.DoubleWidth;
        break;
      case "8": {
        // DECALN
        buf.scrollTop = 0;
        buf.scrollBottom = this.rows - 1;
        buf.marginLeft = 0;
        buf.marginRight = this.cols - 1;
        for (let y = 0; y < this.rows; y++) {
          const l = buf.line(y);
          l.wrapped = false;
          l.lineAttr = LineAttr.Normal;
          l.fill(0, this.cols, 0, 0);
          for (let x = 0; x < this.cols; x++) l.data[x * CELL_SIZE] = 0x45 | WIDTH_1;
        }
        buf.x = 0;
        buf.y = 0;
        this.wrapNext = false;
        this.markAllDirty();
        return;
      }
    }
    this.markDirty(buf.y);
  }

  private vt52Dispatch(final: number): void {
    const buf = this.buf;
    switch (String.fromCharCode(final)) {
      case "A":
        this.cursorUp(1);
        break;
      case "B":
        this.cursorDown(1);
        break;
      case "C":
        this.cursorForward(1);
        break;
      case "D":
        this.cursorBackward(1);
        break;
      case "F":
        this.charsets[0] = DEC_SPECIAL;
        break;
      case "G":
        this.charsets[0] = null;
        break;
      case "H":
        this.cursorTo(0, 0);
        break;
      case "I":
        this.reverseIndex();
        break;
      case "J":
        this.eraseDisplay(0);
        break;
      case "K":
        this.eraseLine(0);
        break;
      case "Y":
        this.parser.captureNext(2, (c) => this.cursorTo(c[1] - 0x20, c[0] - 0x20));
        break;
      case "Z":
        this.send(ESC + "/Z");
        break;
      case "=":
        this.appKeypad = true;
        break;
      case ">":
        this.appKeypad = false;
        break;
      case "<":
        this.vt52 = false;
        this.parser.vt52 = false;
        this.onModeChange();
        break;
    }
    void buf;
  }

  // --------------------------------------------------------------------- CSI

  csiDispatch(prefix: number, collect: number, final: number, p: Params): void {
    const buf = this.buf;
    const ch = String.fromCharCode(final);

    if (collect !== 0) {
      this.csiIntermediate(prefix, collect, ch, p);
      return;
    }

    if (prefix === 0x3f) {
      // "?" private sequences
      switch (ch) {
        case "h":
          for (let i = 0; i < p.length; i++) this.setPrivateMode(p.values[i], true);
          break;
        case "l":
          for (let i = 0; i < p.length; i++) this.setPrivateMode(p.values[i], false);
          break;
        case "s":
          for (let i = 0; i < p.length; i++) {
            const v = this.getPrivateMode(p.values[i]);
            if (v !== null) this.savedModes.set(p.values[i], v);
          }
          break;
        case "r":
          for (let i = 0; i < p.length; i++) {
            const v = this.savedModes.get(p.values[i]);
            if (v !== undefined) this.setPrivateMode(p.values[i], v);
          }
          break;
        case "J":
          this.eraseDisplay(p.get(0, 0), true);
          break;
        case "K":
          this.eraseLine(p.get(0, 0), true);
          break;
        case "n":
          this.deviceStatus(p.get(0, 0), true);
          break;
        case "i":
          this.mediaCopy(p.get(0, 0), true);
          break;
        case "W":
          if (p.get(0, 0) === 5) this.resetTabs();
          break;
        case "c":
          // Linux console cursor shape: CSI ? n c
          if (this.emulation.id === "linux") {
            const n = p.get(0, 0) & 0x0f;
            this.cursorShape = n === 1 ? null : n <= 3 && n >= 2 ? "underline" : n >= 4 ? "block" : null;
            this.cursorVisible = n !== 1;
            this.onModeChange();
          }
          break;
        case "m": // XTQMODKEYS
          break;
      }
      return;
    }

    if (prefix === 0x3e) {
      switch (ch) {
        case "c":
          if (p.get(0, 0) === 0 && this.emulation.da2) this.send(CSI + this.emulation.da2);
          break;
        case "m": // XTMODKEYS: CSI > 4 ; n m
          if (p.get(0, 0) === 4) this.modifyOtherKeys = p.get(1, 0);
          else if (p.length === 0) this.modifyOtherKeys = 0;
          break;
        case "n":
          if (p.get(0, 0) === 4) this.modifyOtherKeys = 0;
          break;
        case "q":
          this.send(ESC + "P>|Hawser(0.1)" + ESC + "\\");
          break;
      }
      return;
    }

    if (prefix === 0x3d) {
      // "=" : SCO ANSI colour and cursor selection, tertiary DA
      switch (ch) {
        case "c":
          this.send(ESC + "P!|00000000" + ESC + "\\");
          break;
        case "F":
          this.fg = (this.fg & ~COLOR_BITS) | CM_PALETTE | (p.get(0, 7) & 0xff);
          break;
        case "G":
          this.bg = (this.bg & ~COLOR_BITS) | CM_PALETTE | (p.get(0, 0) & 0xff);
          break;
        case "C": {
          // cursor shape: start;end scan lines
          const s = p.get(0, 0);
          const e = p.get(1, 15);
          this.cursorVisible = !(s > e);
          this.cursorShape = e - s >= 8 ? "block" : "underline";
          this.onModeChange();
          break;
        }
      }
      return;
    }

    if (prefix !== 0) return; // "<" sequences (kitty keyboard) are not supported

    switch (ch) {
      case "@":
        this.insertChars(p.getNonZero(0, 1));
        break;
      case "A":
      case "k":
        this.cursorUp(p.getNonZero(0, 1));
        break;
      case "B":
      case "e":
        this.cursorDown(p.getNonZero(0, 1));
        break;
      case "C":
      case "a":
        this.cursorForward(p.getNonZero(0, 1));
        break;
      case "D":
      case "j":
        this.cursorBackward(p.getNonZero(0, 1));
        break;
      case "E":
        this.cursorDown(p.getNonZero(0, 1));
        this.carriageReturn();
        break;
      case "F":
        this.cursorUp(p.getNonZero(0, 1));
        this.carriageReturn();
        break;
      case "G":
      case "`":
        this.cursorTo(p.getNonZero(0, 1) - 1, this.originMode ? buf.y - buf.scrollTop : buf.y);
        break;
      case "H":
      case "f":
        this.cursorTo(p.getNonZero(1, 1) - 1, p.getNonZero(0, 1) - 1);
        break;
      case "I":
        this.tab(p.getNonZero(0, 1));
        break;
      case "J":
        this.eraseDisplay(p.get(0, 0));
        break;
      case "K":
        this.eraseLine(p.get(0, 0));
        break;
      case "L":
        this.insertLines(p.getNonZero(0, 1));
        break;
      case "M":
        this.deleteLines(p.getNonZero(0, 1));
        break;
      case "P":
        this.deleteChars(p.getNonZero(0, 1));
        break;
      case "S":
        this.scrollUp(p.getNonZero(0, 1));
        break;
      case "T":
        if (p.length <= 1) this.scrollDown(p.getNonZero(0, 1));
        break;
      case "X":
        this.eraseChars(p.getNonZero(0, 1));
        break;
      case "Z":
        this.backTab(p.getNonZero(0, 1));
        break;
      case "^":
        this.scrollDown(p.getNonZero(0, 1));
        break;
      case "b": {
        // REP
        if (this.lastPrinted >= 0x20) {
          const n = Math.min(p.getNonZero(0, 1), this.cols * this.rows);
          const tmp = new Uint32Array(n).fill(this.lastPrinted);
          const savedCs = this.charsets[this.gl];
          const savedFont = this.altFont;
          this.charsets[this.gl] = null; // lastPrinted is already translated
          this.altFont = 0;
          this.print(tmp, 0, n);
          this.charsets[this.gl] = savedCs;
          this.altFont = savedFont;
        }
        break;
      }
      case "c":
        if (p.get(0, 0) === 0) this.sendDA1();
        break;
      case "d":
        this.cursorTo(this.originMode ? buf.x - (this.leftRightMarginMode ? buf.marginLeft : 0) : buf.x, p.getNonZero(0, 1) - 1);
        break;
      case "g": {
        const m = p.get(0, 0);
        if (m === 0) this.tabs[buf.x] = 0;
        else if (m === 3) this.tabs.fill(0);
        break;
      }
      case "h":
        for (let i = 0; i < p.length; i++) this.setAnsiMode(p.values[i], true);
        break;
      case "l":
        for (let i = 0; i < p.length; i++) this.setAnsiMode(p.values[i], false);
        break;
      case "i":
        this.mediaCopy(p.get(0, 0), false);
        break;
      case "m":
        this.sgr(p);
        break;
      case "n":
        this.deviceStatus(p.get(0, 0), false);
        break;
      case "r": {
        // DECSTBM
        const top = p.getNonZero(0, 1);
        const bottom = Math.min(p.getNonZero(1, this.rows), this.rows);
        if (top < bottom) {
          buf.scrollTop = top - 1;
          buf.scrollBottom = bottom - 1;
          this.cursorTo(0, 0);
        }
        break;
      }
      case "s":
        if (this.leftRightMarginMode) {
          const left = p.getNonZero(0, 1);
          const right = Math.min(p.getNonZero(1, this.cols), this.cols);
          if (left < right) {
            buf.marginLeft = left - 1;
            buf.marginRight = right - 1;
            this.cursorTo(0, 0);
          }
        } else if (p.length === 0) {
          this.saveCursor();
        }
        break;
      case "t":
        this.windowOps(p);
        break;
      case "u":
        this.restoreCursor();
        break;
      case "x":
        // DECREQTPARM
        if (p.get(0, 0) <= 1) this.send(CSI + (p.get(0, 0) + 2) + ";1;1;128;128;1;0x");
        break;
    }
  }

  private csiIntermediate(prefix: number, collect: number, ch: string, p: Params): void {
    const buf = this.buf;
    const inter = String.fromCharCode(collect & 0xff);
    if (collect > 0xff) return;
    switch (inter) {
      case " ":
        switch (ch) {
          case "q": {
            // DECSCUSR
            const n = p.get(0, 0);
            if (n === 0) {
              this.cursorShape = null;
              this.cursorBlink = true;
            } else {
              this.cursorShape = n <= 2 ? "block" : n <= 4 ? "underline" : "bar";
              this.cursorBlink = (n & 1) === 1;
            }
            this.onModeChange();
            break;
          }
          case "@": // SL
            this.scrollColumns(-p.getNonZero(0, 1));
            break;
          case "A": // SR
            this.scrollColumns(p.getNonZero(0, 1));
            break;
        }
        break;
      case "!":
        if (ch === "p") this.softReset();
        break;
      case '"':
        if (ch === "q") {
          // DECSCA
          const n = p.get(0, 0);
          this.protectedMode = n === 1;
          if (this.protectedMode) this.bg |= BG_PROTECTED;
          else this.bg &= ~BG_PROTECTED;
        } else if (ch === "p") {
          // DECSCL: conformance level; 8-bit controls when Ps2 is 0 or 2
          const c1 = p.get(1, 0);
          this.parser.c1 = this.options.c1Controls || (p.get(0, 61) >= 62 && c1 !== 1 && this.options.encoding !== "utf-8");
          this.softReset();
        }
        break;
      case "$":
        this.csiDollar(prefix, ch, p);
        break;
      case "'":
        if (ch === "}") this.shiftColumns(buf.x, p.getNonZero(0, 1)); // DECIC
        else if (ch === "~") this.shiftColumns(buf.x, -p.getNonZero(0, 1)); // DECDC
        break;
      case "*":
        break; // DECSACE / DECRQCRA unsupported
    }
  }

  private scrollColumns(n: number): void {
    const buf = this.buf;
    for (let y = buf.scrollTop; y <= buf.scrollBottom; y++) {
      if (n < 0) buf.line(y).deleteCells(buf.marginLeft, -n, buf.marginRight, this.bg);
      else buf.line(y).insertCells(buf.marginLeft, n, buf.marginRight, this.bg);
    }
    this.markDirty(buf.scrollTop, buf.scrollBottom);
  }

  /** Normalise a rectangle given as 1-based t;l;b;r parameters starting at index i. */
  private rect(p: Params, i: number): [number, number, number, number] | null {
    const buf = this.buf;
    const oy = this.originMode ? buf.scrollTop : 0;
    const ox = this.originMode && this.leftRightMarginMode ? buf.marginLeft : 0;
    const maxY = this.originMode ? buf.scrollBottom : this.rows - 1;
    const maxX = this.originMode && this.leftRightMarginMode ? buf.marginRight : this.cols - 1;
    const t = p.getNonZero(i, 1) - 1 + oy;
    const l = p.getNonZero(i + 1, 1) - 1 + ox;
    const b = Math.min(p.getNonZero(i + 2, this.rows) - 1 + oy, maxY);
    const r = Math.min(p.getNonZero(i + 3, this.cols) - 1 + ox, maxX);
    if (t > b || l > r) return null;
    return [t, l, b, r];
  }

  private csiDollar(prefix: number, ch: string, p: Params): void {
    const buf = this.buf;
    switch (ch) {
      case "p": {
        // DECRQM
        const mode = p.get(0, 0);
        let v: number;
        if (prefix === 0x3f) {
          const m = this.getPrivateMode(mode);
          v = m === null ? 0 : m ? 1 : 2;
          this.send(`${CSI}?${mode};${v}$y`);
        } else {
          const m = this.getAnsiMode(mode);
          v = m === null ? 0 : m ? 1 : 2;
          this.send(`${CSI}${mode};${v}$y`);
        }
        break;
      }
      case "x": {
        // DECFRA: Pch;Pt;Pl;Pb;Pr
        const code = p.get(0, 0x20);
        const r = this.rect(p, 1);
        if (!r || code < 0x20) break;
        for (let y = r[0]; y <= r[2]; y++) {
          const line = buf.line(y);
          for (let x = r[1]; x <= r[3]; x++) line.setCell(x, code | WIDTH_1, this.fg, this.bg, this.ext);
          line.fixWide(r[1]);
          line.fixWide(r[3]);
        }
        this.markDirty(r[0], r[2]);
        break;
      }
      case "z": {
        // DECERA
        const r = this.rect(p, 0);
        if (!r) break;
        for (let y = r[0]; y <= r[2]; y++) buf.line(y).fill(r[1], r[3] + 1, 0, this.bg);
        this.markDirty(r[0], r[2]);
        break;
      }
      case "{": {
        // DECSERA
        const r = this.rect(p, 0);
        if (!r) break;
        for (let y = r[0]; y <= r[2]; y++) this.eraseCells(y, r[1], r[3] + 1, true);
        break;
      }
      case "v": {
        // DECCRA: Pts;Pls;Pbs;Prs;Pps;Ptd;Pld;Ppd
        const r = this.rect(p, 0);
        if (!r) break;
        const oy = this.originMode ? buf.scrollTop : 0;
        const ox = this.originMode && this.leftRightMarginMode ? buf.marginLeft : 0;
        const dt = p.getNonZero(5, 1) - 1 + oy;
        const dl = p.getNonZero(6, 1) - 1 + ox;
        const h = r[2] - r[0] + 1;
        const w = r[3] - r[1] + 1;
        const snap: Line[] = [];
        for (let y = 0; y < h; y++) snap.push(buf.line(r[0] + y).clone());
        for (let y = 0; y < h; y++) {
          const ty = dt + y;
          if (ty >= this.rows) break;
          buf.line(ty).copyFrom(snap[y], r[1], r[1] + w, dl);
          buf.line(ty).fixWide(dl);
          buf.line(ty).fixWide(Math.min(dl + w - 1, this.cols - 1));
        }
        this.markDirty(dt, Math.min(dt + h - 1, this.rows - 1));
        break;
      }
      case "r":
      case "t": {
        // DECCARA / DECRARA: Pt;Pl;Pb;Pr;attrs...
        const r = this.rect(p, 0);
        if (!r) break;
        const reverse = ch === "t";
        for (let i = 4; i < Math.max(p.length, 5); i++) {
          const a = p.get(i, 0);
          let fgBits = 0;
          let clearAll = false;
          switch (a) {
            case 0:
              clearAll = !reverse;
              fgBits = reverse ? FG_BOLD | FG_UNDERLINE | FG_BLINK | FG_INVERSE : 0;
              break;
            case 1:
              fgBits = FG_BOLD;
              break;
            case 4:
              fgBits = FG_UNDERLINE;
              break;
            case 5:
              fgBits = FG_BLINK;
              break;
            case 7:
              fgBits = FG_INVERSE;
              break;
            case 22:
              fgBits = -FG_BOLD;
              break;
            case 24:
              fgBits = -FG_UNDERLINE;
              break;
            case 25:
              fgBits = -FG_BLINK;
              break;
            case 27:
              fgBits = -FG_INVERSE;
              break;
          }
          for (let y = r[0]; y <= r[2]; y++) {
            const d = buf.line(y).data;
            for (let x = r[1]; x <= r[3]; x++) {
              const o = x * CELL_SIZE + 1;
              if (clearAll) d[o] &= ~(FG_BOLD | FG_UNDERLINE | FG_BLINK | FG_INVERSE);
              else if (reverse) d[o] ^= fgBits;
              else if (fgBits < 0) d[o] &= ~-fgBits;
              else d[o] |= fgBits;
            }
          }
        }
        this.markDirty(r[0], r[2]);
        break;
      }
      case "|": {
        // DECSCPP: set columns per page
        const n = p.get(0, 80);
        if (this.options.allowResize && (n === 80 || n === 132 || n === 0)) this.onResizeRequest(n === 0 ? 80 : n, this.rows);
        break;
      }
    }
  }

  // -------------------------------------------------------------------- modes

  private setAnsiMode(mode: number, on: boolean): void {
    switch (mode) {
      case 2:
        this.keyboardLocked = on;
        break;
      case 4:
        this.insertMode = on;
        break;
      case 12:
        this.localEcho = !on;
        this.onModeChange();
        break;
      case 20:
        this.newLineMode = on;
        break;
    }
  }

  private getAnsiMode(mode: number): boolean | null {
    switch (mode) {
      case 2:
        return this.keyboardLocked;
      case 4:
        return this.insertMode;
      case 12:
        return !this.localEcho;
      case 20:
        return this.newLineMode;
    }
    return null;
  }

  private setPrivateMode(mode: number, on: boolean): void {
    const buf = this.buf;
    switch (mode) {
      case 1:
        this.appCursorKeys = on;
        break;
      case 2:
        if (!on) {
          this.vt52 = true;
          this.parser.vt52 = true;
        }
        break;
      case 3:
        if (this.allow132 && this.options.allowResize) {
          this.onResizeRequest(on ? 132 : 80, this.rows);
          const b = this.buf;
          b.scrollTop = 0;
          b.scrollBottom = this.rows - 1;
          b.marginLeft = 0;
          b.marginRight = this.cols - 1;
          this.eraseDisplay(2);
          this.cursorTo(0, 0);
        }
        break;
      case 4:
        break; // smooth scroll
      case 5:
        if (this.reverseVideo !== on) {
          this.reverseVideo = on;
          this.markAllDirty();
        }
        break;
      case 6:
        this.originMode = on;
        this.cursorTo(0, 0);
        break;
      case 7:
        this.autoWrap = on;
        if (!on) this.wrapNext = false;
        break;
      case 8:
        break; // auto repeat
      case 9:
        this.mouseMode = on ? MouseMode.X10 : MouseMode.None;
        break;
      case 12:
        this.cursorBlink = on;
        break;
      case 25:
        this.cursorVisible = on;
        this.markDirty(buf.y);
        break;
      case 40:
        this.allow132 = on;
        break;
      case 45:
        this.reverseWrap = on;
        break;
      case 47:
        this.useAltScreen(on, false);
        break;
      case 66:
        this.appKeypad = on;
        break;
      case 67:
        this.backarrowSendsBS = on;
        break;
      case 69:
        this.leftRightMarginMode = on;
        if (!on) {
          buf.marginLeft = 0;
          buf.marginRight = this.cols - 1;
        }
        break;
      case 1000:
        this.mouseMode = on ? MouseMode.Normal : MouseMode.None;
        break;
      case 1002:
        this.mouseMode = on ? MouseMode.ButtonEvent : MouseMode.None;
        break;
      case 1003:
        this.mouseMode = on ? MouseMode.AnyEvent : MouseMode.None;
        break;
      case 1004:
        this.focusEvents = on;
        break;
      case 1005:
        this.mouseEncoding = on ? MouseEncoding.Utf8 : MouseEncoding.Default;
        break;
      case 1006:
        this.mouseEncoding = on ? MouseEncoding.Sgr : MouseEncoding.Default;
        break;
      case 1015:
        this.mouseEncoding = on ? MouseEncoding.Urxvt : MouseEncoding.Default;
        break;
      case 1016:
        this.mouseEncoding = on ? MouseEncoding.SgrPixels : MouseEncoding.Default;
        break;
      case 1007:
        this.altScroll = on;
        break;
      case 1034:
      case 1036:
        this.metaSendsEscape = on;
        break;
      case 1047:
        if (!on && this.buf === this.alt) for (let y = 0; y < this.rows; y++) this.alt.line(y).fill(0, this.cols, 0, this.bg);
        this.useAltScreen(on, false);
        break;
      case 1048:
        if (on) this.saveCursor();
        else this.restoreCursor();
        break;
      case 1049:
        if (on) {
          this.saveCursor();
          this.useAltScreen(true, true);
        } else {
          this.useAltScreen(false, false);
          this.restoreCursor();
        }
        break;
      case 2004:
        this.bracketedPaste = on;
        break;
      case 2026:
        this.syncOutput = on;
        if (!on) this.markAllDirty();
        break;
      default:
        return;
    }
    this.onModeChange();
  }

  private getPrivateMode(mode: number): boolean | null {
    switch (mode) {
      case 1:
        return this.appCursorKeys;
      case 2:
        return !this.vt52;
      case 3:
        return this.cols === 132;
      case 5:
        return this.reverseVideo;
      case 6:
        return this.originMode;
      case 7:
        return this.autoWrap;
      case 9:
        return this.mouseMode === MouseMode.X10;
      case 12:
        return this.cursorBlink;
      case 25:
        return this.cursorVisible;
      case 40:
        return this.allow132;
      case 45:
        return this.reverseWrap;
      case 47:
      case 1047:
      case 1049:
        return this.buf === this.alt;
      case 66:
        return this.appKeypad;
      case 67:
        return this.backarrowSendsBS === true;
      case 69:
        return this.leftRightMarginMode;
      case 1000:
        return this.mouseMode === MouseMode.Normal;
      case 1002:
        return this.mouseMode === MouseMode.ButtonEvent;
      case 1003:
        return this.mouseMode === MouseMode.AnyEvent;
      case 1004:
        return this.focusEvents;
      case 1005:
        return this.mouseEncoding === MouseEncoding.Utf8;
      case 1006:
        return this.mouseEncoding === MouseEncoding.Sgr;
      case 1015:
        return this.mouseEncoding === MouseEncoding.Urxvt;
      case 1016:
        return this.mouseEncoding === MouseEncoding.SgrPixels;
      case 1007:
        return this.altScroll;
      case 1034:
      case 1036:
        return this.metaSendsEscape;
      case 2004:
        return this.bracketedPaste;
      case 2026:
        return this.syncOutput;
    }
    return null;
  }

  // ---------------------------------------------------------------------- SGR

  private sgr(p: Params): void {
    if (p.length === 0) {
      this.resetAttrs();
      return;
    }
    const color = this.options.ansiColor;
    const v = p.values;
    const sub = p.sub;
    const n = p.length;
    for (let i = 0; i < n; i++) {
      const a = v[i] < 0 ? 0 : v[i];
      // collect colon sub-parameters belonging to this parameter
      let subEnd = i + 1;
      while (subEnd < n && sub[subEnd]) subEnd++;
      const hasSub = subEnd > i + 1;

      if (a >= 30 && a <= 37) {
        if (color) this.fg = (this.fg & ~COLOR_BITS) | CM_PALETTE | (a - 30);
      } else if (a >= 40 && a <= 47) {
        if (color) this.bg = (this.bg & ~COLOR_BITS) | CM_PALETTE | (a - 40);
      } else if (a >= 90 && a <= 97) {
        if (color) this.fg = (this.fg & ~COLOR_BITS) | CM_PALETTE | (a - 90 + 8);
      } else if (a >= 100 && a <= 107) {
        if (color) this.bg = (this.bg & ~COLOR_BITS) | CM_PALETTE | (a - 100 + 8);
      } else
        switch (a) {
          case 0:
            this.resetAttrs();
            break;
          case 1:
            this.fg |= FG_BOLD;
            break;
          case 2:
            this.bg |= BG_DIM;
            break;
          case 3:
            this.bg |= BG_ITALIC;
            break;
          case 4:
            if (hasSub) {
              const style = v[i + 1] < 0 ? 1 : v[i + 1];
              if (style === 0) {
                this.fg &= ~FG_UNDERLINE;
                this.setExt({ underlineStyle: UnderlineStyle.None });
              } else {
                this.fg |= FG_UNDERLINE;
                this.setExt({ underlineStyle: Math.min(style, 5) as UnderlineStyle });
              }
            } else {
              this.fg |= FG_UNDERLINE;
              this.setExt({ underlineStyle: UnderlineStyle.Single });
            }
            break;
          case 5:
          case 6:
            this.fg |= FG_BLINK;
            break;
          case 7:
            this.fg |= FG_INVERSE;
            break;
          case 8:
            this.fg |= FG_INVISIBLE;
            break;
          case 9:
            this.fg |= FG_STRIKE;
            break;
          case 10:
          case 11:
          case 12:
            this.altFont = a;
            break;
          case 21:
            this.fg |= FG_UNDERLINE;
            this.setExt({ underlineStyle: UnderlineStyle.Double });
            break;
          case 22:
            this.fg &= ~FG_BOLD;
            this.bg &= ~BG_DIM;
            break;
          case 23:
            this.bg &= ~BG_ITALIC;
            break;
          case 24:
            this.fg &= ~FG_UNDERLINE;
            this.setExt({ underlineStyle: UnderlineStyle.None });
            break;
          case 25:
            this.fg &= ~FG_BLINK;
            break;
          case 27:
            this.fg &= ~FG_INVERSE;
            break;
          case 28:
            this.fg &= ~FG_INVISIBLE;
            break;
          case 29:
            this.fg &= ~FG_STRIKE;
            break;
          case 38:
          case 48:
          case 58: {
            let c = -1;
            if (hasSub) {
              const mode = v[i + 1];
              if (mode === 5 && subEnd - i >= 3) c = CM_PALETTE | (v[i + 2] & 0xff);
              else if (mode === 2) {
                // 38:2:r:g:b or 38:2:<colourspace>:r:g:b
                const k = subEnd - i >= 6 ? i + 3 : i + 2;
                if (k + 2 < subEnd) c = CM_RGB | ((v[k] & 0xff) << 16) | ((v[k + 1] & 0xff) << 8) | (v[k + 2] & 0xff);
              }
            } else if (i + 1 < n) {
              const mode = v[i + 1];
              if (mode === 5 && i + 2 < n) {
                c = CM_PALETTE | (Math.max(0, v[i + 2]) & 0xff);
                i += 2;
              } else if (mode === 2 && i + 4 < n) {
                c = CM_RGB | ((Math.max(0, v[i + 2]) & 0xff) << 16) | ((Math.max(0, v[i + 3]) & 0xff) << 8) | (Math.max(0, v[i + 4]) & 0xff);
                i += 4;
              } else {
                i = n; // malformed: stop processing
              }
            }
            if (c >= 0 && color) {
              if (a === 38) this.fg = (this.fg & ~COLOR_BITS) | c;
              else if (a === 48) this.bg = (this.bg & ~COLOR_BITS) | c;
              else this.setExt({ underlineColor: c });
            }
            break;
          }
          case 39:
            this.fg = (this.fg & ~COLOR_BITS) | CM_DEFAULT;
            break;
          case 49:
            this.bg = (this.bg & ~COLOR_BITS) | CM_DEFAULT;
            break;
          case 53:
            this.bg |= BG_OVERLINE;
            break;
          case 55:
            this.bg &= ~BG_OVERLINE;
            break;
          case 59:
            this.setExt({ underlineColor: -1 });
            break;
        }
      if (hasSub) i = subEnd - 1;
    }
  }

  private resetAttrs(): void {
    this.fg = 0;
    this.bg = this.protectedMode ? BG_PROTECTED : 0;
    this.altFont = 0;
    if (this.ext) {
      const link = this.ext.link;
      this.ext = link ? new ExtAttr(UnderlineStyle.None, -1, link) : null;
    }
  }

  private setExt(patch: Partial<{ underlineStyle: UnderlineStyle; underlineColor: number; link: number }>): void {
    const next = (this.ext ?? new ExtAttr()).with(patch);
    this.ext = next.isEmpty() ? null : next;
  }

  // ------------------------------------------------------------------ reports

  private sendDA1(): void {
    if (this.emulation.da1) this.send(CSI + this.emulation.da1);
  }

  private deviceStatus(n: number, priv: boolean): void {
    const buf = this.buf;
    const row = (this.originMode ? buf.y - buf.scrollTop : buf.y) + 1;
    const col = (this.originMode && this.leftRightMarginMode ? buf.x - buf.marginLeft : buf.x) + 1;
    if (!priv) {
      if (n === 5) this.send(CSI + "0n");
      else if (n === 6) this.send(`${CSI}${row};${col}R`);
      return;
    }
    switch (n) {
      case 6:
        this.send(`${CSI}?${row};${col};1R`);
        break;
      case 15:
        this.send(CSI + "?10n"); // printer ready
        break;
      case 25:
        this.send(CSI + "?20n"); // UDK unlocked
        break;
      case 26:
        this.send(CSI + "?27;1;0;0n"); // keyboard: North American
        break;
      case 53:
      case 55:
        this.send(CSI + "?50n"); // no locator
        break;
    }
  }

  private windowOps(p: Params): void {
    const op = p.get(0, 0);
    switch (op) {
      case 8: {
        if (!this.options.allowResize) break;
        const rows = p.get(1, 0) || this.rows;
        const cols = p.get(2, 0) || this.cols;
        this.onResizeRequest(cols, rows);
        break;
      }
      case 11:
        this.send(CSI + "1t");
        break;
      case 13:
        this.send(CSI + "3;0;0t");
        break;
      case 14:
        this.send(`${CSI}4;${this.rows * 16};${this.cols * 8}t`);
        break;
      case 16:
        this.send(CSI + "6;16;8t");
        break;
      case 18:
      case 19:
        this.send(`${CSI}${op === 18 ? 8 : 9};${this.rows};${this.cols}t`);
        break;
      case 20:
        this.send(ESC + "]L" + ESC + "\\"); // never disclose titles
        break;
      case 21:
        this.send(ESC + "]l" + ESC + "\\");
        break;
      case 22:
        if (this.titleStack.length < 16) this.titleStack.push(this.title);
        break;
      case 23: {
        const t = this.titleStack.pop();
        if (t !== undefined) this.setTitle(t);
        break;
      }
      default:
        if (op >= 24 && this.options.allowResize) this.onResizeRequest(this.cols, op); // DECSLPP
    }
  }

  private setTitle(t: string): void {
    if (!this.options.allowTitleChange) return;
    // strip control characters
    t = t.replace(/[\u0000-\u001f\u007f-\u009f]/g, "");
    this.title = t;
    this.onTitle(t);
  }

  // ------------------------------------------------------------------ printer

  private mediaCopy(n: number, priv: boolean): void {
    if (priv) {
      switch (n) {
        case 1: // print cursor line
          this.onPrint(this.buf.line(this.buf.y).text(0, this.cols, true) + "\n");
          break;
        case 4:
          this.printerMode = 0;
          break;
        case 5:
          this.printerMode = 2;
          break;
      }
      return;
    }
    switch (n) {
      case 0: {
        const out: string[] = [];
        for (let y = 0; y < this.rows; y++) out.push(this.buf.line(y).text(0, this.cols, true));
        this.onPrint(out.join("\n") + "\n\f");
        break;
      }
      case 4:
        if (this.printerMode === 1) {
          this.printerMode = 0;
          const data = this.printBuf.join("");
          this.printBuf = [];
          if (data) this.onPrint(data);
        }
        break;
      case 5:
        this.printerMode = 1;
        this.printBuf = [];
        break;
    }
  }

  private autoPrintLine(): void {
    this.onPrint(this.buf.line(this.buf.y).text(0, this.cols, true) + "\n");
  }

  /** True while printer-controller (pass-through) mode is active. */
  get printing(): boolean {
    return this.printerMode === 1;
  }

  // ---------------------------------------------------------------------- OSC

  oscDispatch(data: string): void {
    if (data.startsWith("linuxP;")) {
      const s = data.slice(7);
      const idx = parseInt(s[0], 16);
      const rgb = parseInt(s.slice(1), 16);
      if (!isNaN(idx) && !isNaN(rgb) && s.length === 7) {
        this.colors.palette.set(idx, rgb);
        this.markAllDirty();
        this.onColorChange();
      }
      return;
    }
    if (data === "linuxR") {
      this.colors.palette.clear();
      this.markAllDirty();
      this.onColorChange();
      return;
    }
    const semi = data.indexOf(";");
    const id = parseInt(semi < 0 ? data : data.slice(0, semi), 10);
    const arg = semi < 0 ? "" : data.slice(semi + 1);
    switch (id) {
      case 0:
        this.iconName = arg;
        this.setTitle(arg);
        break;
      case 1:
        this.iconName = arg;
        break;
      case 2:
        this.setTitle(arg);
        break;
      case 4: {
        const parts = arg.split(";");
        for (let i = 0; i + 1 < parts.length; i += 2) {
          const idx = parseInt(parts[i], 10);
          if (isNaN(idx) || idx < 0 || idx > 255) continue;
          if (parts[i + 1] === "?") {
            this.send(`${ESC}]4;${idx};${formatColor(this.currentColor(idx))}${ESC}\\`);
          } else {
            const c = parseColor(parts[i + 1]);
            if (c !== null) this.colors.palette.set(idx, c);
          }
        }
        this.markAllDirty();
        this.onColorChange();
        break;
      }
      case 7:
        this.cwd = arg;
        break;
      case 8: {
        const s2 = arg.indexOf(";");
        const params = s2 < 0 ? "" : arg.slice(0, s2);
        const uri = s2 < 0 ? arg : arg.slice(s2 + 1);
        if (!uri) {
          this.setExt({ link: 0 });
        } else {
          const m = /(?:^|:)id=([^:]*)/.exec(params);
          const lid = m ? m[1] : "";
          let idx = this.links.length - 1;
          const last = this.links[idx];
          if (idx === 0 || last.uri !== uri || last.id !== lid) {
            if (this.links.length > 60000) this.links.length = 1;
            this.links.push({ uri: uri.slice(0, 4096), id: lid });
            idx = this.links.length - 1;
          }
          this.setExt({ link: idx });
        }
        break;
      }
      case 10:
      case 11:
      case 12: {
        const parts = arg.split(";");
        let which = id;
        for (const part of parts) {
          const key = which === 10 ? "foreground" : which === 11 ? "background" : "cursor";
          if (part === "?") {
            const c = this.colors[key] ?? this.colorQuery(which === 10 ? "fg" : which === 11 ? "bg" : "cursor");
            this.send(`${ESC}]${which};${formatColor(c)}${ESC}\\`);
          } else {
            const c = parseColor(part);
            if (c !== null) this.colors[key] = c;
          }
          if (++which > 12) break;
        }
        this.markAllDirty();
        this.onColorChange();
        break;
      }
      case 52: {
        const s2 = arg.indexOf(";");
        if (s2 < 0) break;
        const payload = arg.slice(s2 + 1);
        if (payload === "?") break; // never disclose the clipboard
        if (!this.options.allowClipboardWrite) break;
        try {
          const bin = atob(payload);
          const bytes = new Uint8Array(bin.length);
          for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
          this.onClipboard(new TextDecoder().decode(bytes));
        } catch {
          /* invalid base64 */
        }
        break;
      }
      case 104:
        if (!arg) this.colors.palette.clear();
        else for (const s of arg.split(";")) this.colors.palette.delete(parseInt(s, 10));
        this.markAllDirty();
        this.onColorChange();
        break;
      case 110:
        this.colors.foreground = null;
        this.markAllDirty();
        this.onColorChange();
        break;
      case 111:
        this.colors.background = null;
        this.markAllDirty();
        this.onColorChange();
        break;
      case 112:
        this.colors.cursor = null;
        this.onColorChange();
        break;
    }
  }

  private currentColor(idx: number): number {
    return this.colors.palette.get(idx) ?? this.colorQuery(idx);
  }

  // ---------------------------------------------------------------------- DCS

  dcsDispatch(prefix: number, collect: number, final: number, _p: Params, data: string): void {
    const ch = String.fromCharCode(final);
    if (collect === 0x24 && ch === "q") {
      // DECRQSS
      let reply: string | null = null;
      const buf = this.buf;
      switch (data) {
        case "m":
          reply = this.sgrReport() + "m";
          break;
        case "r":
          reply = `${buf.scrollTop + 1};${buf.scrollBottom + 1}r`;
          break;
        case "s":
          reply = `${buf.marginLeft + 1};${buf.marginRight + 1}s`;
          break;
        case " q": {
          const shape = this.cursorShape ?? "block";
          const base = shape === "block" ? 1 : shape === "underline" ? 3 : 5;
          reply = `${base + (this.cursorBlink ? 0 : 1)} q`;
          break;
        }
        case '"p':
          reply = `6${Math.max(1, this.emulation.level)};1"p`;
          break;
        case '"q':
          reply = `${this.protectedMode ? 1 : 0}"q`;
          break;
      }
      if (reply !== null) this.send(`${ESC}P1$r${reply}${ESC}\\`);
      else this.send(`${ESC}P0$r${ESC}\\`);
      return;
    }
    if (collect === 0x2b && ch === "q") {
      // XTGETTCAP
      const out: string[] = [];
      for (const hex of data.split(";")) {
        const name = hexDecode(hex);
        let value: string | null = null;
        if (name === "TN" || name === "name") value = this.emulation.termType;
        else if (name === "Co" || name === "colors") value = "256";
        else if (name === "RGB") value = "8/8/8";
        if (value === null) {
          this.send(`${ESC}P0+r${hex}${ESC}\\`);
          return;
        }
        out.push(`${hex}=${hexEncode(value)}`);
      }
      this.send(`${ESC}P1+r${out.join(";")}${ESC}\\`);
      return;
    }
    void prefix;
  }

  private sgrReport(): string {
    const out = ["0"];
    if (this.fg & FG_BOLD) out.push("1");
    if (this.bg & BG_DIM) out.push("2");
    if (this.bg & BG_ITALIC) out.push("3");
    if (this.fg & FG_UNDERLINE) out.push("4");
    if (this.fg & FG_BLINK) out.push("5");
    if (this.fg & FG_INVERSE) out.push("7");
    if (this.fg & FG_INVISIBLE) out.push("8");
    if (this.fg & FG_STRIKE) out.push("9");
    const col = (word: number, base: number) => {
      const cm = word & 0x3000000;
      const c = word & 0xffffff;
      if (cm === CM_PALETTE) {
        if (c < 8) out.push(String(base + c));
        else if (c < 16) out.push(String(base + 60 + c - 8));
        else out.push(`${base + 8};5;${c}`);
      } else if (cm === CM_RGB) out.push(`${base + 8};2;${c >> 16};${(c >> 8) & 0xff};${c & 0xff}`);
    };
    col(this.fg, 30);
    col(this.bg, 40);
    return out.join(";");
  }

  stringDispatch(_kind: number, _data: string): void {
    // SOS / PM / APC strings are consumed and ignored
  }

  // ------------------------------------------------------------- text access

  /** Text of a screen row (0-based), trailing blanks removed. */
  rowText(y: number): string {
    const l = this.buf.line(y);
    return l ? l.text(0, this.cols, true) : "";
  }

  /** Screen contents as lines (used by tests and scripting). */
  screenText(): string[] {
    const out: string[] = [];
    for (let y = 0; y < this.rows; y++) out.push(this.rowText(y));
    return out;
  }
}

/** Hooks that let an alternative control language (Wyse) take over parsing results. */
export interface DialectHandler {
  print(data: Uint32Array, start: number, end: number): boolean;
  execute(code: number): boolean;
  esc(collect: number, final: number): boolean;
}

function hexDecode(h: string): string {
  let s = "";
  for (let i = 0; i + 1 < h.length; i += 2) s += String.fromCharCode(parseInt(h.slice(i, i + 2), 16));
  return s;
}

function hexEncode(s: string): string {
  let h = "";
  for (let i = 0; i < s.length; i++) h += s.charCodeAt(i).toString(16).padStart(2, "0").toUpperCase();
  return h;
}

/** Parse an X11 colour spec (rgb:rr/gg/bb, #rrggbb, #rgb) to 0xRRGGBB. */
export function parseColor(s: string): number | null {
  s = s.trim();
  let m = /^rgb:([0-9a-f]{1,4})\/([0-9a-f]{1,4})\/([0-9a-f]{1,4})$/i.exec(s);
  if (m) {
    const ch = (h: string) => Math.round((parseInt(h, 16) / (Math.pow(16, h.length) - 1)) * 255);
    return (ch(m[1]) << 16) | (ch(m[2]) << 8) | ch(m[3]);
  }
  m = /^#([0-9a-f]+)$/i.exec(s);
  if (m) {
    const h = m[1];
    if (h.length === 3) return (parseInt(h[0] + h[0], 16) << 16) | (parseInt(h[1] + h[1], 16) << 8) | parseInt(h[2] + h[2], 16);
    if (h.length === 6) return parseInt(h, 16);
    if (h.length === 12) return (parseInt(h.slice(0, 2), 16) << 16) | (parseInt(h.slice(4, 6), 16) << 8) | parseInt(h.slice(8, 10), 16);
  }
  return null;
}

function formatColor(c: number): string {
  const h = (v: number) => {
    const s = v.toString(16).padStart(2, "0");
    return s + s;
  };
  return `rgb:${h((c >> 16) & 0xff)}/${h((c >> 8) & 0xff)}/${h(c & 0xff)}`;
}

export { CM_DEFAULT, CM_PALETTE, CM_RGB, CODE_MASK };
