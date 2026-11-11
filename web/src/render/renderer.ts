// Canvas renderer for the terminal screen model.

import {
  BG_DIM, BG_EXT, BG_ITALIC, BG_OVERLINE, CELL_SIZE, CM_MASK, CM_PALETTE, CM_RGB, CODE_MASK, COLOR_MASK, COMBINED,
  FG_BLINK, FG_BOLD, FG_INVERSE, FG_INVISIBLE, FG_STRIKE, FG_UNDERLINE, Line, LineAttr, UnderlineStyle, WIDTH_MASK, WIDTH_SHIFT,
} from "../term/cell";
import { Theme, blend, css } from "../term/palette";
import { CursorShape, Terminal } from "../term/terminal";
import { drawSpecialGlyph, isSpecialGlyph } from "./glyphs";

export interface RenderOptions {
  fontFamily: string;
  fontSize: number; // CSS px
  lineHeight: number;
  boldAsBright: boolean;
  boldFont: boolean;
  blink: boolean;
  cursorShape: CursorShape;
  cursorBlink: boolean;
}

/** A decorated span on one row (search hits, keyword highlights, links). */
export interface RowDecoration {
  start: number;
  end: number; // exclusive
  fg?: number;
  bg?: number;
  bold?: boolean;
  underline?: boolean;
}

export interface SelectionRange {
  /** absolute line numbers (buffer.trimmed + index) */
  startLine: number;
  startCol: number;
  endLine: number;
  endCol: number; // inclusive
  block: boolean;
}

export class Renderer {
  readonly ctx: CanvasRenderingContext2D;
  theme: Theme;
  opts: RenderOptions;
  cellW = 8;
  cellH = 16;
  private baseline = 12;
  private dpr = 1;
  private fontPx = 14;
  private fonts: string[] = [];
  private lastFont = "";
  private line1 = 1;

  focused = true;
  cursorOn = true;
  blinkOn = true;
  selection: SelectionRange | null = null;
  /** decorations for a viewport row; set by the controller */
  decorate: ((line: Line, absLine: number) => RowDecoration[] | null) | null = null;
  private fullRedraw = true;
  private lastGen = -1;
  private lastOffset = -1;
  private lastCursorRow = -1;
  private blinkRows = new Set<number>();
  private drawnBuf: unknown = null;

  constructor(
    readonly canvas: HTMLCanvasElement,
    public term: Terminal,
    theme: Theme,
    opts: RenderOptions,
  ) {
    this.ctx = canvas.getContext("2d", { alpha: false })!;
    this.theme = theme;
    this.opts = opts;
    this.measure();
  }

  setOptions(opts: RenderOptions): void {
    this.opts = opts;
    this.measure();
    this.invalidate();
  }

  setTheme(theme: Theme): void {
    this.theme = theme;
    this.invalidate();
  }

  invalidate(): void {
    this.fullRedraw = true;
  }

  /** Recompute cell metrics for the current font and device pixel ratio. */
  measure(): void {
    this.dpr = Math.max(1, window.devicePixelRatio || 1);
    this.fontPx = Math.max(6, Math.round(this.opts.fontSize * this.dpr));
    const fam = this.opts.fontFamily;
    const bold = this.opts.boldFont ? "bold " : "";
    this.fonts = [
      `${this.fontPx}px ${fam}`,
      `${bold}${this.fontPx}px ${fam}`,
      `italic ${this.fontPx}px ${fam}`,
      `italic ${bold}${this.fontPx}px ${fam}`,
    ];
    const ctx = this.ctx;
    ctx.font = this.fonts[0];
    this.lastFont = this.fonts[0];
    const m = ctx.measureText("W");
    const asc = m.fontBoundingBoxAscent ?? this.fontPx * 0.8;
    const desc = m.fontBoundingBoxDescent ?? this.fontPx * 0.2;
    this.cellW = Math.max(1, Math.round(m.width));
    this.cellH = Math.max(1, Math.ceil((asc + desc) * this.opts.lineHeight));
    this.baseline = Math.round((this.cellH - (asc + desc)) / 2 + asc);
    this.line1 = Math.max(1, Math.round(this.fontPx / 14));
  }

  /** Columns and rows that fit into a CSS-pixel box. */
  fit(width: number, height: number): { cols: number; rows: number } {
    return {
      cols: Math.max(2, Math.floor((width * this.dpr) / this.cellW)),
      rows: Math.max(1, Math.floor((height * this.dpr) / this.cellH)),
    };
  }

  /** Size the canvas for the terminal's current dimensions. */
  resizeCanvas(): void {
    const w = this.term.cols * this.cellW;
    const h = this.term.rows * this.cellH;
    if (this.canvas.width !== w || this.canvas.height !== h) {
      this.canvas.width = w;
      this.canvas.height = h;
    }
    this.canvas.style.width = w / this.dpr + "px";
    this.canvas.style.height = h / this.dpr + "px";
    this.lastFont = "";
    this.invalidate();
  }

  get cssCellWidth(): number {
    return this.cellW / this.dpr;
  }
  get cssCellHeight(): number {
    return this.cellH / this.dpr;
  }

  private color(word: number, isFg: boolean): number {
    const cm = word & CM_MASK;
    if (cm === CM_RGB) return word & COLOR_MASK;
    const t = this.term;
    if (cm === CM_PALETTE) {
      const idx = word & 0xff;
      return t.colors.palette.get(idx) ?? this.theme.palette[idx];
    }
    return isFg ? (t.colors.foreground ?? this.theme.foreground) : (t.colors.background ?? this.theme.background);
  }

  render(): void {
    const t = this.term;
    if (t.syncOutput && !this.fullRedraw) return;
    const buf = t.buf;
    let [top, bottom] = t.consumeDirty();
    if (this.fullRedraw || this.lastGen !== t.scrollGeneration || this.lastOffset !== buf.scrollOffset || this.drawnBuf !== buf) {
      top = 0;
      bottom = t.rows - 1;
    }
    this.fullRedraw = false;
    this.lastGen = t.scrollGeneration;
    this.lastOffset = buf.scrollOffset;
    this.drawnBuf = buf;

    const cursorRow = buf.scrollOffset + buf.y; // viewport row of the cursor
    const rowsToDraw = new Set<number>();
    for (let y = top; y <= bottom; y++) rowsToDraw.add(y + buf.scrollOffset);
    // the dirty range is in screen rows; when scrolled back everything shifts
    if (buf.scrollOffset > 0 && bottom >= top) for (let y = 0; y < t.rows; y++) rowsToDraw.add(y);
    if (this.lastCursorRow >= 0) rowsToDraw.add(this.lastCursorRow);
    rowsToDraw.add(cursorRow);
    this.lastCursorRow = cursorRow;

    const vt = buf.viewportTop;
    for (const y of rowsToDraw) {
      if (y < 0 || y >= t.rows) continue;
      this.drawRow(y, buf.lines[vt + y], buf.trimmed + vt + y);
    }
    if (cursorRow >= 0 && cursorRow < t.rows) this.drawCursor(cursorRow);
  }

  /** Redraw rows containing blinking text (called when the blink phase flips). */
  redrawBlink(): void {
    const buf = this.term.buf;
    const vt = buf.viewportTop;
    for (const y of Array.from(this.blinkRows)) {
      if (y < this.term.rows) this.drawRow(y, buf.lines[vt + y], buf.trimmed + vt + y);
    }
    const cr = buf.scrollOffset + buf.y;
    if (this.blinkRows.has(cr)) this.drawCursor(cr);
  }

  redrawCursorRow(): void {
    const buf = this.term.buf;
    const cr = buf.scrollOffset + buf.y;
    if (cr < 0 || cr >= this.term.rows) return;
    this.drawRow(cr, buf.lines[buf.viewportTop + cr], buf.trimmed + buf.viewportTop + cr);
    this.drawCursor(cr);
  }

  private selRange(absLine: number, cols: number): [number, number] | null {
    const s = this.selection;
    if (!s || absLine < s.startLine || absLine > s.endLine) return null;
    if (s.block) return [Math.min(s.startCol, s.endCol), Math.max(s.startCol, s.endCol)];
    const a = absLine === s.startLine ? s.startCol : 0;
    const b = absLine === s.endLine ? s.endCol : cols - 1;
    return a <= b ? [a, b] : null;
  }

  private setFont(i: number): void {
    const f = this.fonts[i];
    if (f !== this.lastFont) {
      this.ctx.font = f;
      this.lastFont = f;
    }
  }

  private drawRow(y: number, line: Line | undefined, absLine: number): void {
    const ctx = this.ctx;
    const t = this.term;
    const cols = t.cols;
    const cw = this.cellW;
    const ch = this.cellH;
    const py = y * ch;
    const defBg = t.colors.background ?? this.theme.background;
    const defFg = t.colors.foreground ?? this.theme.foreground;
    const screenBg = t.reverseVideo ? defFg : defBg;
    ctx.fillStyle = css(screenBg);
    ctx.fillRect(0, py, cols * cw, ch);
    this.blinkRows.delete(y);
    if (!line) return;

    const d = line.data;
    const n = Math.min(cols, line.cols);
    const sel = this.selRange(absLine, cols);
    const decos = this.decorate ? this.decorate(line, absLine) : null;
    const dbl = line.lineAttr !== LineAttr.Normal;
    const limit = dbl ? Math.ceil(n / 2) : n;
    const selBg = this.theme.selection;

    if (dbl) {
      // double-width / double-height lines: draw at 2x and clip to the row
      ctx.save();
      ctx.beginPath();
      ctx.rect(0, py, cols * cw, ch);
      ctx.clip();
      const dh = line.lineAttr !== LineAttr.DoubleWidth;
      ctx.translate(0, line.lineAttr === LineAttr.DoubleHeightBottom ? py - ch : py);
      ctx.scale(2, dh ? 2 : 1);
    }
    const rowY = dbl ? 0 : py;

    // ---- pass 1: backgrounds; pass 2: glyphs and decorations
    const fgs = new Uint32Array(limit);
    let hasBlink = false;
    for (let x = 0; x < limit; x++) {
      const o = x * CELL_SIZE;
      const fgw = d[o + 1];
      const bgw = d[o + 2];
      let fg = this.color(fgw, true);
      let bg = this.color(bgw, false);
      if (this.opts.boldAsBright && fgw & FG_BOLD && (fgw & CM_MASK) === CM_PALETTE && (fgw & 0xff) < 8) {
        const idx = (fgw & 0xff) + 8;
        fg = t.colors.palette.get(idx) ?? this.theme.palette[idx];
      }
      let inverse = (fgw & FG_INVERSE) !== 0;
      if (t.reverseVideo) inverse = !inverse;
      if (inverse) {
        const tmp = fg;
        fg = bg;
        bg = tmp;
      }
      if (bgw & BG_DIM) fg = blend(fg, bg, 0.6);
      if (decos) {
        for (const dc of decos) {
          if (x >= dc.start && x < dc.end) {
            if (dc.bg !== undefined) bg = dc.bg;
            if (dc.fg !== undefined) fg = dc.fg;
          }
        }
      }
      if (sel && x >= sel[0] && x <= sel[1]) {
        bg = selBg;
        // keep text readable on the selection colour
        fg = this.theme.foreground;
      }
      if (fgw & FG_INVISIBLE) fg = bg;
      if (fgw & FG_BLINK) {
        hasBlink = true;
        if (this.opts.blink && !this.blinkOn) fg = bg;
      }
      fgs[x] = fg;
      if (bg !== screenBg) {
        ctx.fillStyle = css(bg);
        ctx.fillRect(x * cw, rowY, cw, ch);
      }
    }
    if (hasBlink) this.blinkRows.add(y);
    // selection beyond the text extent of short lines
    if (sel && sel[1] >= limit) {
      ctx.fillStyle = css(selBg);
      const a = Math.max(sel[0], limit);
      ctx.fillRect(a * cw, rowY, (sel[1] - a + 1) * cw, ch);
    }

    const cellHh = ch;
    const base = this.baseline;
    ctx.textBaseline = "alphabetic";
    let lastFill = -1;
    for (let x = 0; x < limit; x++) {
      const o = x * CELL_SIZE;
      const c = d[o];
      const w = (c & WIDTH_MASK) >>> WIDTH_SHIFT;
      if (w === 0) continue;
      const fgw = d[o + 1];
      const bgw = d[o + 2];
      const code = c & CODE_MASK;
      const fg = fgs[x];
      const px = x * cw;
      let deco: RowDecoration | null = null;
      if (decos) for (const dc of decos) if (x >= dc.start && x < dc.end && (dc.underline || dc.bold)) deco = dc;
      if (code > 0x20 || c & COMBINED) {
        if (fg !== lastFill) {
          ctx.fillStyle = css(fg);
          lastFill = fg;
        }
        if (!(c & COMBINED) && isSpecialGlyph(code)) {
          drawSpecialGlyph(ctx, code, px, rowY, cw * w, cellHh, this.line1, css(fg), css(this.cellBg(d, o, sel, x, screenBg)));
          lastFill = -1;
        } else {
          const bold = (fgw & FG_BOLD) !== 0 || deco?.bold === true;
          this.setFont((bold ? 1 : 0) | (bgw & BG_ITALIC ? 2 : 0));
          const text = c & COMBINED ? (line.combined?.get(x) ?? "") : code > 0xffff ? String.fromCodePoint(code) : String.fromCharCode(code);
          ctx.fillText(text, px, rowY + base);
        }
      }
      // decorations
      const ul = fgw & FG_UNDERLINE || deco?.underline;
      if (ul || fgw & FG_STRIKE || bgw & BG_OVERLINE) {
        const lw = this.line1;
        const cellW = cw * w;
        if (ul) {
          let style = UnderlineStyle.Single;
          let ucol = fg;
          if (bgw & BG_EXT) {
            const e = line.ext?.get(x);
            if (e) {
              if (e.underlineStyle) style = e.underlineStyle;
              if (e.underlineColor !== -1) ucol = this.color(e.underlineColor, true);
            }
          }
          ctx.fillStyle = css(ucol);
          lastFill = -1;
          const uy = rowY + Math.min(cellHh - lw, base + Math.max(1, Math.round(this.fontPx * 0.12)));
          if (style === UnderlineStyle.Double) {
            ctx.fillRect(px, uy - lw, cellW, lw);
            ctx.fillRect(px, Math.min(rowY + cellHh - lw, uy + lw), cellW, lw);
          } else if (style === UnderlineStyle.Curly) {
            for (let i = 0; i < cellW; i += lw) ctx.fillRect(px + i, uy + Math.round(Math.sin((i / cellW) * Math.PI * 2) * lw), lw, lw);
          } else if (style === UnderlineStyle.Dotted || style === UnderlineStyle.Dashed) {
            const seg = style === UnderlineStyle.Dotted ? lw : Math.max(2, Math.round(cellW / 4));
            for (let i = 0; i < cellW; i += seg * 2) ctx.fillRect(px + i, uy, Math.min(seg, cellW - i), lw);
          } else {
            ctx.fillRect(px, uy, cellW, lw);
          }
        }
        if (fgw & FG_STRIKE) {
          ctx.fillStyle = css(fg);
          lastFill = -1;
          ctx.fillRect(px, rowY + Math.round(base * 0.68), cellW, lw);
        }
        if (bgw & BG_OVERLINE) {
          ctx.fillStyle = css(fg);
          lastFill = -1;
          ctx.fillRect(px, rowY, cellW, lw);
        }
      }
    }
    if (dbl) ctx.restore();
  }

  private cellBg(d: Uint32Array, o: number, sel: [number, number] | null, x: number, screenBg: number): number {
    if (sel && x >= sel[0] && x <= sel[1]) return this.theme.selection;
    const fgw = d[o + 1];
    let inverse = (fgw & FG_INVERSE) !== 0;
    if (this.term.reverseVideo) inverse = !inverse;
    const c = inverse ? this.color(fgw, true) : this.color(d[o + 2], false);
    return c === undefined ? screenBg : c;
  }

  private drawCursor(vy: number): void {
    const t = this.term;
    const buf = t.buf;
    if (!t.cursorVisible) return;
    const blink = this.opts.cursorBlink && t.cursorBlink;
    if (this.focused && blink && !this.cursorOn) return;
    const ctx = this.ctx;
    const line = buf.line(buf.y);
    const dbl = line && line.lineAttr !== LineAttr.Normal ? 2 : 1;
    let x = Math.min(buf.x, t.cols - 1);
    if (line && line.width(x) === 0 && x > 0) x--;
    const w = (line && line.width(x) === 2 ? 2 : 1) * this.cellW * dbl;
    const px = x * this.cellW * dbl;
    const py = vy * this.cellH;
    const col = t.colors.cursor ?? this.theme.cursor;
    const shape = t.cursorShape ?? this.opts.cursorShape;
    ctx.fillStyle = css(col);
    if (!this.focused) {
      const lw = this.line1;
      ctx.fillRect(px, py, w, lw);
      ctx.fillRect(px, py + this.cellH - lw, w, lw);
      ctx.fillRect(px, py, lw, this.cellH);
      ctx.fillRect(px + w - lw, py, lw, this.cellH);
      return;
    }
    if (shape === "underline") {
      ctx.fillRect(px, py + this.cellH - Math.max(2, this.line1 * 2), w, Math.max(2, this.line1 * 2));
    } else if (shape === "bar") {
      ctx.fillRect(px, py, Math.max(2, this.line1 * 2), this.cellH);
    } else {
      ctx.fillRect(px, py, w, this.cellH);
      // redraw the character in the background colour on top of the block
      if (line && dbl === 1) {
        const c = line.data[x * CELL_SIZE];
        const code = c & CODE_MASK;
        if (code > 0x20 || c & COMBINED) {
          const bg = t.colors.background ?? this.theme.background;
          ctx.fillStyle = css(bg);
          const fgw = line.data[x * CELL_SIZE + 1];
          this.setFont((fgw & FG_BOLD ? 1 : 0) | (line.data[x * CELL_SIZE + 2] & BG_ITALIC ? 2 : 0));
          if (!(c & COMBINED) && isSpecialGlyph(code)) {
            drawSpecialGlyph(ctx, code, px, py, w, this.cellH, this.line1, css(bg), css(col));
          } else {
            ctx.textBaseline = "alphabetic";
            ctx.fillText(line.cellText(x), px, py + this.baseline);
          }
        }
      }
    }
  }
}
