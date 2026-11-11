// TerminalController glues one connection to an emulator, a renderer and the DOM.

import { Renderer, RenderOptions, RowDecoration } from "../render/renderer";
import { rpc } from "../rpc";
import { Line } from "../term/cell";
import { encodeText } from "../term/charsets";
import { emulationById } from "../term/emulation";
import { comboName, encodeKey, keyInfo } from "../term/keyboard";
import { encodeMouse } from "../term/mouse";
import { ColorScheme, buildTheme, hexToRgb, schemeById } from "../term/palette";
import { MouseMode, Terminal } from "../term/terminal";
import { Match, Pos, SearchOptions, buildSearch, comparePos, findLinks, findNext, getText, matchLine, wordAt } from "../term/text";

export interface Keyword {
  pattern: string;
  regex?: boolean;
  caseSensitive?: boolean;
  fg?: string;
  bg?: string;
  bold?: boolean;
  underline?: boolean;
}

export interface SessionOptions {
  protocol: string;
  terminal: any;
  appearance: any;
  [k: string]: any;
}

export interface ControllerHost {
  /** resolve appearance defaults from global options */
  defaults(): { fontFamily: string; fontSize: number; colorScheme: string; customSchemes: ColorScheme[] };
  keywords(setName: string): Keyword[];
  /** app-level shortcut handling; return true when the key was consumed */
  shortcut(combo: string, e: KeyboardEvent, ctrl: TerminalController): boolean;
  contextMenu(e: MouseEvent, ctrl: TerminalController): void;
  confirmPaste(text: string): Promise<boolean>;
  notify(kind: "title" | "bell" | "activity" | "selection" | "cursor" | "size" | "print", ctrl: TerminalController, data?: any): void;
}

const DEFAULT_FONT = '"JetBrains Mono", "Cascadia Mono", "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace';

export class TerminalController {
  readonly term: Terminal;
  renderer!: Renderer;
  opts: SessionOptions;
  connected = false;
  title = "";

  private root: HTMLElement | null = null;
  private canvas!: HTMLCanvasElement;
  private input!: HTMLTextAreaElement;
  private thumb!: HTMLElement;
  private track!: HTMLElement;
  private raf = 0;
  private timers: number[] = [];
  private ro: ResizeObserver | null = null;
  private pendingAck = 0;
  private composing = false;
  private keywordRes: { re: RegExp; kw: Keyword }[] = [];

  // selection
  private selAnchor: Pos | null = null;
  private selHead: Pos | null = null;
  private selMode: "char" | "word" | "line" | "block" = "char";
  private selecting = false;
  private mouseButtons = 0;

  // search
  private searchRe: RegExp | null = null;
  private currentMatch: Match | null = null;
  private hoverLink: { line: number; start: number; end: number; url: string } | null = null;
  /** local echo forced by the transport (telnet without remote echo) */
  forcedEcho = false;
  locked = false;

  constructor(
    readonly connId: number,
    opts: SessionOptions,
    private readonly host: ControllerHost,
  ) {
    this.opts = opts;
    const t = opts.terminal;
    this.term = new Terminal({
      cols: t.cols || 80, rows: t.rows || 24, scrollback: t.scrollback, emulation: t.emulation, encoding: t.encoding,
      answerback: t.answerback, newLineMode: t.newLineMode, autoWrap: t.autoWrap, ansiColor: t.ansiColor,
      allowResize: t.allow132, allowTitleChange: t.allowTitleChange, allowClipboardWrite: t.allowClipboardWrite, c1Controls: t.c1Controls,
    });
    const term = this.term;
    term.onData = (s) => this.sendBytes(encodeText(s, "iso-8859-1"));
    term.onTitle = (title) => {
      this.title = title;
      host.notify("title", this);
    };
    term.onBell = () => host.notify("bell", this);
    term.onClipboard = (text) => navigator.clipboard?.writeText(text).catch(() => {});
    term.onPrint = (text) => host.notify("print", this, text);
    term.onResizeRequest = (cols, rows) => {
      if (t.sizeMode === "fixed" || t.allow132) this.setSize(cols, rows);
    };
    term.colorQuery = (w) => {
      const th = this.renderer?.theme;
      if (!th) return 0;
      return w === "fg" ? th.foreground : w === "bg" ? th.background : w === "cursor" ? th.cursor : th.palette[w];
    };
    rpc.onData(connId, (p) => this.onData(p));
    this.setKeywords();
  }

  // ------------------------------------------------------------ appearance

  private renderOptions(): RenderOptions {
    const a = this.opts.appearance;
    const d = this.host.defaults();
    return {
      fontFamily: a.fontFamily ? `${a.fontFamily}, ${DEFAULT_FONT}` : d.fontFamily || DEFAULT_FONT,
      fontSize: a.fontSize || d.fontSize || 14,
      lineHeight: a.lineHeight || 1,
      boldAsBright: a.boldAsBright, boldFont: a.boldFont, blink: a.blink,
      cursorShape: this.opts.terminal.cursorStyle || "block",
      cursorBlink: this.opts.terminal.cursorBlink,
    };
  }

  private scheme(): ColorScheme {
    const d = this.host.defaults();
    return schemeById(this.opts.appearance.colorScheme || d.colorScheme, d.customSchemes);
  }

  /** Re-read options (after the session or global options were edited). */
  applyOptions(opts?: SessionOptions): void {
    if (opts) this.opts = opts;
    const t = this.opts.terminal;
    const o = this.term.options;
    if (o.emulation !== t.emulation) this.term.setEmulation(t.emulation);
    if (o.encoding !== t.encoding) this.term.setEncoding(t.encoding);
    if (o.scrollback !== t.scrollback) this.term.setScrollback(t.scrollback);
    Object.assign(o, { answerback: t.answerback, ansiColor: t.ansiColor, allowResize: t.allow132, allowTitleChange: t.allowTitleChange, allowClipboardWrite: t.allowClipboardWrite });
    this.setKeywords();
    if (this.renderer) {
      this.renderer.setTheme(buildTheme(this.scheme()));
      this.renderer.setOptions(this.renderOptions());
      this.layout();
    }
  }

  setKeywords(): void {
    this.keywordRes = [];
    for (const kw of this.host.keywords(this.opts.terminal.keywordSet || "")) {
      const re = buildSearch(kw.pattern, { caseSensitive: !!kw.caseSensitive, regex: !!kw.regex });
      if (re) this.keywordRes.push({ re, kw });
    }
    this.renderer?.invalidate();
    this.schedule();
  }

  get backgroundCss(): string {
    return this.scheme().background;
  }

  // ------------------------------------------------------------------ DOM

  attach(root: HTMLElement): void {
    this.root = root;
    root.innerHTML = "";
    root.classList.add("term-root");
    this.canvas = document.createElement("canvas");
    this.input = document.createElement("textarea");
    this.input.className = "term-input";
    this.input.setAttribute("autocapitalize", "off");
    this.input.setAttribute("autocorrect", "off");
    this.input.setAttribute("spellcheck", "false");
    this.input.setAttribute("aria-label", "Terminal input");
    this.track = document.createElement("div");
    this.track.className = "term-scroll";
    this.thumb = document.createElement("div");
    this.thumb.className = "term-thumb";
    this.track.appendChild(this.thumb);
    root.append(this.canvas, this.input, this.track);
    root.style.background = this.backgroundCss;

    this.renderer = new Renderer(this.canvas, this.term, buildTheme(this.scheme()), this.renderOptions());
    this.renderer.decorate = (line, abs) => this.decorations(line, abs);
    this.bind();
    this.ro = new ResizeObserver(() => this.layout());
    this.ro.observe(root);
    document.fonts?.ready.then(() => {
      this.renderer.measure();
      this.layout();
    });
    this.timers.push(window.setInterval(() => this.blinkTick(), 530));
    this.layout();
  }

  detach(): void {
    this.ro?.disconnect();
    this.ro = null;
    for (const t of this.timers) clearInterval(t);
    this.timers = [];
    cancelAnimationFrame(this.raf);
    this.raf = 0;
    this.root = null;
  }

  dispose(): void {
    this.detach();
    rpc.onData(this.connId, null);
  }

  focus(): void {
    this.input?.focus({ preventScroll: true });
  }

  private blinkPhase = 0;
  private blinkTick(): void {
    if (!this.root || !this.root.isConnected) return;
    const r = this.renderer;
    r.cursorOn = !r.cursorOn;
    r.redrawCursorRow();
    if (++this.blinkPhase % 2 === 0) {
      r.blinkOn = !r.blinkOn;
      r.redrawBlink();
    }
  }

  /** Fit the terminal to its container (or keep a fixed size). */
  layout(): void {
    if (!this.root || !this.renderer) return;
    const w = this.root.clientWidth - 12; // scrollbar gutter
    const h = this.root.clientHeight;
    if (w <= 0 || h <= 0) return;
    const t = this.opts.terminal;
    if (t.sizeMode === "fixed") {
      this.renderer.resizeCanvas();
    } else {
      const { cols, rows } = this.renderer.fit(w - 4, h - 2);
      this.setSize(cols, rows);
    }
    this.schedule();
  }

  setSize(cols: number, rows: number): void {
    if (cols !== this.term.cols || rows !== this.term.rows) {
      this.term.resize(cols, rows);
      rpc.notify("conn.resize", { id: this.connId, cols, rows });
      this.clearSelection();
      this.host.notify("size", this);
    }
    this.renderer?.resizeCanvas();
    this.schedule();
  }

  schedule(): void {
    if (this.raf || !this.root) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      if (!this.root) return;
      this.renderer.render();
      this.updateScrollbar();
      this.host.notify("cursor", this);
    });
  }

  private updateScrollbar(): void {
    const buf = this.term.buf;
    const total = buf.lines.length;
    const rows = this.term.rows;
    if (buf.scrollbackLength === 0) {
      this.thumb.style.display = "none";
      return;
    }
    const th = this.track.clientHeight;
    const size = Math.max(24, (rows / total) * th);
    const top = ((buf.viewportTop) / Math.max(1, total - rows)) * (th - size);
    this.thumb.style.display = "block";
    this.thumb.style.height = size + "px";
    this.thumb.style.transform = `translateY(${top}px)`;
  }

  // ----------------------------------------------------------------- data

  private onData(p: Uint8Array): void {
    const buf = this.term.buf;
    const wasBottom = buf.scrollOffset === 0;
    this.term.write(p);
    if (!wasBottom && this.opts.terminal.scrollOnOutput) buf.scrollOffset = 0;
    this.pendingAck += p.length;
    if (this.pendingAck >= 32768) this.flushAck();
    else if (this.pendingAck === p.length) setTimeout(() => this.flushAck(), 50);
    this.schedule();
    this.host.notify("activity", this);
  }

  private flushAck(): void {
    if (this.pendingAck > 0) {
      rpc.notify("conn.ack", { id: this.connId, bytes: this.pendingAck });
      this.pendingAck = 0;
    }
  }

  /** Print a local status line (connection events) into the terminal. */
  status(text: string, kind: "info" | "error" | "ok" = "info"): void {
    const color = kind === "error" ? "31" : kind === "ok" ? "32" : "36";
    const lead = this.term.buf.x > 0 ? "\r\n" : "";
    this.term.writeString(`${lead}\x1b[0;${color}m${text}\x1b[0m\r\n`);
    this.schedule();
  }

  sendBytes(data: Uint8Array): void {
    if (!this.connected || this.locked || this.term.keyboardLocked || data.length === 0) return;
    rpc.sendInput(this.connId, data);
  }

  /** Send typed/pasted text in the session encoding (with optional local echo). */
  sendText(s: string): void {
    if (!s) return;
    if (this.opts.terminal.scrollOnKey) this.scrollToBottom();
    this.sendBytes(encodeText(s, this.opts.terminal.encoding));
    if ((this.opts.terminal.localEcho || this.term.localEcho || this.forcedEcho) && this.connected) {
      this.term.writeString(s.replace(/\r(?!\n)/g, "\r\n"));
      this.schedule();
    }
  }

  async paste(text?: string): Promise<void> {
    if (text === undefined) {
      try {
        text = await navigator.clipboard.readText();
      } catch {
        return;
      }
    }
    if (!text) return;
    const t = this.opts.terminal;
    text = text.replace(/\r\n/g, "\n");
    if (t.trimPaste) text = text.replace(/[ \t]+$/gm, "").replace(/\n+$/, "");
    if (t.confirmMultilinePaste && /\n/.test(text.replace(/\n$/, "")) && !this.term.bracketedPaste) {
      if (!(await this.host.confirmPaste(text))) return;
    }
    const nl = t.newlineOnSend === "lf" ? "\n" : t.newlineOnSend === "crlf" ? "\r\n" : "\r";
    let out = text.replace(/\n/g, nl);
    if (this.term.bracketedPaste) out = "\x1b[200~" + out.replace(/\x1b/g, "") + "\x1b[201~";
    if (t.lineSendDelay > 0 || t.charSendDelay > 0) {
      if (this.connected) rpc.notify("conn.send", { id: this.connId, text: out, charDelay: t.charSendDelay, lineDelay: t.lineSendDelay });
    } else this.sendText(out);
    this.focus();
  }

  // ------------------------------------------------------------- keyboard

  private bind(): void {
    const root = this.root!;
    const input = this.input;
    input.addEventListener("keydown", (e) => this.onKeyDown(e));
    input.addEventListener("compositionstart", () => (this.composing = true));
    input.addEventListener("compositionend", (e) => {
      this.composing = false;
      if (e.data) this.sendText(e.data);
      input.value = "";
    });
    input.addEventListener("input", (e) => {
      if (this.composing || (e as InputEvent).isComposing) return;
      const v = input.value;
      input.value = "";
      if (v && (e as InputEvent).inputType !== "insertFromPaste") this.sendText(v);
    });
    input.addEventListener("paste", (e) => {
      e.preventDefault();
      this.paste(e.clipboardData?.getData("text/plain") ?? "");
    });
    input.addEventListener("focus", () => this.setFocused(true));
    input.addEventListener("blur", () => this.setFocused(false));

    root.addEventListener("mousedown", (e) => this.onMouseDown(e));
    root.addEventListener("mousemove", (e) => this.onMouseMove(e));
    root.addEventListener("wheel", (e) => this.onWheel(e), { passive: false });
    root.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      if (this.mouseReporting(e)) return;
      if (this.opts.terminal.rightClick === "paste") this.paste();
      else this.host.contextMenu(e, this);
    });
    root.addEventListener("auxclick", (e) => {
      if (e.button === 1 && !this.mouseReporting(e) && this.opts.terminal.middleClick === "paste") {
        e.preventDefault();
        this.paste();
      }
    });
    this.thumb.addEventListener("mousedown", (e) => this.onThumbDown(e));
    this.track.addEventListener("mousedown", (e) => {
      if (e.target !== this.track) return;
      e.stopPropagation();
      const r = this.thumb.getBoundingClientRect();
      this.scrollLines(e.clientY < r.top ? -this.term.rows : this.term.rows);
    });
  }

  private setFocused(f: boolean): void {
    this.renderer.focused = f;
    this.renderer.cursorOn = true;
    this.root?.classList.toggle("focused", f);
    if (this.term.focusEvents && this.connected) this.sendBytes(encodeText(f ? "\x1b[I" : "\x1b[O", "utf-8"));
    this.renderer.redrawCursorRow();
  }

  private onKeyDown(e: KeyboardEvent): void {
    if (e.isComposing || e.keyCode === 229) return;
    const k = keyInfo(e);
    const combo = comboName(k);
    if (this.host.shortcut(combo, e, this)) {
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    // session key map
    for (const mk of this.opts.terminal.keymap ?? []) {
      if (mk.key === combo) {
        e.preventDefault();
        if (mk.action === "send") rpc.notify("conn.send", { id: this.connId, text: mk.value, escapes: true });
        return;
      }
    }
    // scrollback navigation
    if (k.shift && !k.ctrl && !k.alt && !this.term.isAltScreen) {
      if (k.key === "PageUp") return e.preventDefault(), this.scrollLines(-(this.term.rows - 1));
      if (k.key === "PageDown") return e.preventDefault(), this.scrollLines(this.term.rows - 1);
    }
    const altGr = e.getModifierState?.("AltGraph");
    if (altGr && k.key.length === 1) return; // composed character arrives via input event
    const t = this.opts.terminal;
    const em = emulationById(t.emulation);
    const seq = encodeKey(k, {
      family: em.keys, appCursor: this.term.appCursorKeys, appKeypad: this.term.appKeypad, newLineMode: this.term.newLineMode,
      backspaceIsBS: this.term.backarrowSendsBS ?? t.backspace === "bs", deleteIsDel: t.deleteKey === "del",
      altIsMeta: t.altIsMeta, vt52: this.term.vt52,
    });
    if (seq !== null) {
      e.preventDefault();
      if (!this.connected && k.key === "Enter") {
        this.host.shortcut("@reconnect", e, this);
        return;
      }
      this.clearSelection();
      this.sendText(seq);
    } else if (k.key.length === 1 && !k.ctrl && !k.meta && !k.alt) {
      this.clearSelection();
    }
  }

  // ---------------------------------------------------------------- mouse

  private cellAt(e: MouseEvent): { col: number; row: number; px: number; py: number } {
    const r = this.canvas.getBoundingClientRect();
    const px = e.clientX - r.left;
    const py = e.clientY - r.top;
    const col = Math.min(this.term.cols - 1, Math.max(0, Math.floor(px / this.renderer.cssCellWidth)));
    const row = Math.min(this.term.rows - 1, Math.max(0, Math.floor(py / this.renderer.cssCellHeight)));
    return { col, row, px, py };
  }

  private posAt(e: MouseEvent): Pos {
    const { col, row } = this.cellAt(e);
    const buf = this.term.buf;
    return { line: buf.trimmed + buf.viewportTop + row, col };
  }

  private mouseReporting(e: MouseEvent | WheelEvent): boolean {
    return this.term.mouseMode !== MouseMode.None && !e.shiftKey && this.opts.terminal.allowMouseReports !== false && this.connected;
  }

  private report(e: MouseEvent, kind: "down" | "up" | "move" | "wheel", button: number): void {
    const c = this.cellAt(e);
    const dpr = window.devicePixelRatio || 1;
    const s = encodeMouse({ button, kind, col: c.col, row: c.row, px: c.px * dpr, py: c.py * dpr, shift: e.shiftKey, alt: e.altKey, ctrl: e.ctrlKey },
      this.term.mouseMode, this.term.mouseEncoding);
    if (s) this.sendBytes(encodeText(s, "utf-8"));
  }

  private lastCell = "";
  private onMouseDown(e: MouseEvent): void {
    if (e.target === this.thumb || e.target === this.track) return;
    e.preventDefault();
    this.focus();
    if (this.mouseReporting(e)) {
      this.mouseButtons |= 1 << e.button;
      this.report(e, "down", e.button);
      const up = (ev: MouseEvent) => {
        this.mouseButtons &= ~(1 << ev.button);
        this.report(ev, "up", ev.button);
        window.removeEventListener("mouseup", up);
      };
      window.addEventListener("mouseup", up);
      return;
    }
    if (e.button !== 0) return;
    if ((e.ctrlKey || e.metaKey) && this.hoverLink) {
      window.open(this.hoverLink.url, "_blank", "noopener,noreferrer");
      return;
    }
    const pos = this.posAt(e);
    const buf = this.term.buf;
    const line = buf.lines[pos.line - buf.trimmed];
    if (e.shiftKey && this.selAnchor) {
      this.selHead = pos;
    } else if (e.detail >= 3) {
      this.selMode = "line";
      this.selAnchor = { line: pos.line, col: 0 };
      this.selHead = { line: pos.line, col: this.term.cols - 1 };
    } else if (e.detail === 2 && line) {
      this.selMode = "word";
      const [a, b] = wordAt(line, pos.col, this.opts.terminal.wordDelimiters ?? " ");
      this.selAnchor = { line: pos.line, col: a };
      this.selHead = { line: pos.line, col: b };
    } else {
      this.selMode = e.altKey ? "block" : "char";
      this.selAnchor = pos;
      this.selHead = null;
    }
    this.selecting = true;
    const anchor0 = this.selAnchor!;
    const head0 = this.selHead;
    const move = (ev: MouseEvent) => {
      const p = this.posAt(ev);
      const r = this.canvas.getBoundingClientRect();
      if (ev.clientY < r.top) this.scrollLines(-1);
      else if (ev.clientY > r.bottom) this.scrollLines(1);
      if (this.selMode === "word" || this.selMode === "line") {
        const ln = this.term.buf.lines[p.line - this.term.buf.trimmed];
        const [a, b] = this.selMode === "line" || !ln ? [0, this.term.cols - 1] : wordAt(ln, p.col, this.opts.terminal.wordDelimiters ?? " ");
        if (comparePos(p, anchor0) < 0) {
          this.selAnchor = head0 ?? anchor0;
          this.selHead = { line: p.line, col: a };
        } else {
          this.selAnchor = anchor0;
          this.selHead = { line: p.line, col: b };
        }
      } else this.selHead = p;
      this.syncSelection();
    };
    const up = () => {
      this.selecting = false;
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      if (this.hasSelection() && this.opts.terminal.copyOnSelect) this.copy();
      this.host.notify("selection", this);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
    this.syncSelection();
  }

  private onMouseMove(e: MouseEvent): void {
    if (this.mouseReporting(e)) {
      const c = this.cellAt(e);
      const key = c.col + "," + c.row;
      if (key === this.lastCell) return;
      this.lastCell = key;
      const b = this.mouseButtons & 1 ? 0 : this.mouseButtons & 2 ? 1 : this.mouseButtons & 4 ? 2 : 3;
      this.report(e, "move", b);
      return;
    }
    if (this.selecting) return;
    // link hover (Ctrl/Cmd + click opens)
    const pos = this.posAt(e);
    const buf = this.term.buf;
    const line = buf.lines[pos.line - buf.trimmed];
    let link: typeof this.hoverLink = null;
    if (line && (e.ctrlKey || e.metaKey)) {
      const ext = line.ext?.get(pos.col);
      if (ext?.link) {
        const uri = this.term.links[ext.link]?.uri;
        let a = pos.col, b = pos.col;
        while (a > 0 && line.ext?.get(a - 1)?.link === ext.link) a--;
        while (b < line.cols - 1 && line.ext?.get(b + 1)?.link === ext.link) b++;
        if (uri && /^(https?|ftp|file|mailto):/i.test(uri)) link = { line: pos.line, start: a, end: b + 1, url: uri };
      } else {
        for (const [s, en, url] of findLinks(line, this.term.cols)) if (pos.col >= s && pos.col < en) link = { line: pos.line, start: s, end: en, url };
      }
    }
    if ((link?.url ?? "") + (link?.line ?? "") !== (this.hoverLink?.url ?? "") + (this.hoverLink?.line ?? "")) {
      this.hoverLink = link;
      this.root!.style.cursor = link ? "pointer" : "";
      this.renderer?.invalidate();
      this.schedule();
    }
  }

  private wheelAcc = 0;
  private onWheel(e: WheelEvent): void {
    e.preventDefault();
    if (e.ctrlKey) {
      this.host.shortcut(e.deltaY < 0 ? "@zoomIn" : "@zoomOut", e as any, this);
      return;
    }
    const unit = e.deltaMode === 1 ? 1 : e.deltaMode === 2 ? this.term.rows : 1 / (this.renderer.cssCellHeight || 16);
    this.wheelAcc += e.deltaY * unit;
    const lines = Math.trunc(this.wheelAcc);
    if (lines === 0) return;
    this.wheelAcc -= lines;
    if (this.mouseReporting(e)) {
      for (let i = 0; i < Math.min(Math.abs(lines), 5); i++) this.report(e, "down", lines < 0 ? 64 : 65);
    } else if (this.term.isAltScreen && this.term.altScroll && this.connected) {
      const key = lines < 0 ? "A" : "B";
      this.sendBytes(encodeText(((this.term.appCursorKeys ? "\x1bO" : "\x1b[") + key).repeat(Math.min(Math.abs(lines), 10)), "utf-8"));
    } else this.scrollLines(lines);
  }

  private onThumbDown(e: MouseEvent): void {
    e.preventDefault();
    e.stopPropagation();
    const buf = this.term.buf;
    const startY = e.clientY;
    const startTop = buf.viewportTop;
    const th = this.track.clientHeight - this.thumb.clientHeight;
    const max = buf.lines.length - this.term.rows;
    const move = (ev: MouseEvent) => {
      const top = Math.round(startTop + ((ev.clientY - startY) / Math.max(1, th)) * max);
      buf.scrollOffset = Math.min(Math.max(max - top, 0), buf.scrollbackLength);
      this.schedule();
    };
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  }

  // -------------------------------------------------------------- scrolling

  scrollLines(n: number): void {
    const buf = this.term.buf;
    buf.scrollOffset = Math.min(Math.max(buf.scrollOffset - n, 0), buf.scrollbackLength);
    this.schedule();
  }

  scrollToBottom(): void {
    if (this.term.buf.scrollOffset !== 0) {
      this.term.buf.scrollOffset = 0;
      this.schedule();
    }
  }

  scrollToTop(): void {
    this.term.buf.scrollOffset = this.term.buf.scrollbackLength;
    this.schedule();
  }

  // -------------------------------------------------------------- selection

  private syncSelection(): void {
    const a = this.selAnchor;
    const h = this.selHead;
    if (!this.renderer) return;
    if (!a || !h) this.renderer.selection = null;
    else {
      const [s, e] = comparePos(a, h) <= 0 ? [a, h] : [h, a];
      this.renderer.selection = { startLine: s.line, startCol: s.col, endLine: e.line, endCol: e.col, block: this.selMode === "block" };
    }
    this.renderer?.invalidate();
    this.schedule();
  }

  hasSelection(): boolean {
    return !!(this.selAnchor && this.selHead);
  }

  clearSelection(): void {
    if (this.selAnchor || this.selHead) {
      this.selAnchor = this.selHead = null;
      this.syncSelection();
    }
  }

  selectAll(): void {
    const buf = this.term.buf;
    this.selMode = "char";
    this.selAnchor = { line: buf.trimmed, col: 0 };
    this.selHead = { line: buf.trimmed + buf.lines.length - 1, col: this.term.cols - 1 };
    this.syncSelection();
  }

  selectionText(): string {
    if (!this.selAnchor || !this.selHead) return "";
    return getText(this.term.buf, this.selAnchor, this.selHead, this.selMode === "block", this.term.cols);
  }

  async copy(): Promise<boolean> {
    const text = this.selectionText();
    if (!text) return false;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // clipboard API unavailable (insecure context): fall back to execCommand
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
      this.focus();
    }
    return true;
  }

  /** Whole buffer (scrollback + screen) as text. */
  allText(): string {
    const buf = this.term.buf;
    return getText(buf, { line: buf.trimmed, col: 0 }, { line: buf.trimmed + buf.lines.length - 1, col: this.term.cols - 1 }, false, this.term.cols);
  }

  // ----------------------------------------------------------------- search

  setSearch(query: string, o: SearchOptions): boolean {
    this.searchRe = buildSearch(query, o);
    this.currentMatch = null;
    this.renderer?.invalidate();
    this.schedule();
    return !!this.searchRe || !query;
  }

  find(backward: boolean): boolean {
    if (!this.searchRe) return false;
    const buf = this.term.buf;
    const from: Pos = this.currentMatch
      ? { line: this.currentMatch.line, col: this.currentMatch.start }
      : { line: buf.trimmed + buf.viewportTop + (backward ? this.term.rows - 1 : 0), col: backward ? this.term.cols : -1 };
    const m = findNext(buf, this.term.cols, this.searchRe, from, backward);
    this.currentMatch = m;
    if (m) {
      const idx = m.line - buf.trimmed;
      const vt = buf.viewportTop;
      if (idx < vt || idx >= vt + this.term.rows) {
        const top = Math.max(0, Math.min(idx - Math.floor(this.term.rows / 2), buf.lines.length - this.term.rows));
        buf.scrollOffset = buf.lines.length - this.term.rows - top;
      }
    }
    this.renderer?.invalidate();
    this.schedule();
    return !!m;
  }

  private decorations(line: Line, abs: number): RowDecoration[] | null {
    let out: RowDecoration[] | null = null;
    const cols = this.term.cols;
    for (const { re, kw } of this.keywordRes) {
      for (const m of matchLine(line, abs, cols, re)) {
        (out ??= []).push({ start: m.start, end: m.end, fg: kw.fg ? hexToRgb(kw.fg) : undefined, bg: kw.bg ? hexToRgb(kw.bg) : undefined, bold: kw.bold, underline: kw.underline });
      }
    }
    if (this.searchRe) {
      for (const m of matchLine(line, abs, cols, this.searchRe)) {
        const cur = this.currentMatch && this.currentMatch.line === abs && this.currentMatch.start === m.start;
        (out ??= []).push({ start: m.start, end: m.end, bg: cur ? 0xff9d2e : 0x8a6d1f, fg: cur ? 0x1a1a1a : 0xffffff });
      }
    }
    const hl = this.hoverLink;
    if (hl && hl.line === abs) (out ??= []).push({ start: hl.start, end: hl.end, underline: true });
    return out;
  }

  // ------------------------------------------------------------------ misc

  reset(): void {
    this.term.reset();
    this.schedule();
  }

  clearScreen(scrollback: boolean): void {
    if (scrollback) this.term.clearScrollback();
    this.term.clearScreen();
    this.schedule();
  }

  /** Screen text for scripting: rows/cols are 1-based and inclusive. */
  screenGet(r1: number, c1: number, r2: number, c2: number): string {
    const out: string[] = [];
    for (let r = r1; r <= r2; r++) {
      const line = this.term.buf.line(r - 1);
      out.push(line ? line.text(c1 - 1, c2, false).padEnd(c2 - c1 + 1) : "");
    }
    return out.join("\n");
  }
}
