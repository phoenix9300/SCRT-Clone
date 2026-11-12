import { BG_EXT, CELL_SIZE, COMBINED, ExtAttr, Line, LineAttr, WIDTH_MASK, WIDTH_SHIFT } from "./cell";

/** Saved cursor state (DECSC / DECRC). */
export interface SavedCursor {
  x: number;
  y: number;
  fg: number;
  bg: number;
  ext: ExtAttr | null;
  originMode: boolean;
  wrapNext: boolean;
  gl: number;
  gr: number;
  charsets: (import("./charsets").Charset)[];
  protectedMode: boolean;
}

/**
 * A screen buffer: scrollback lines followed by the visible rows.
 * The visible screen is always the last `rows` entries of `lines`.
 */
export class Buffer {
  lines: Line[] = [];
  /** cursor position relative to the top of the screen */
  x = 0;
  y = 0;
  scrollTop = 0;
  scrollBottom: number;
  marginLeft = 0;
  marginRight: number;
  saved: SavedCursor | null = null;
  /** number of lines the viewport is scrolled back (0 = following output) */
  scrollOffset = 0;
  /** total number of lines discarded from the top of the scrollback */
  trimmed = 0;

  constructor(
    public cols: number,
    public rows: number,
    public maxScrollback: number,
  ) {
    this.scrollBottom = rows - 1;
    this.marginRight = cols - 1;
    for (let i = 0; i < rows; i++) this.lines.push(new Line(cols));
  }

  /** index in `lines` of the first visible screen row */
  get ybase(): number {
    return this.lines.length - this.rows;
  }

  /** screen row accessor */
  line(y: number): Line {
    return this.lines[this.lines.length - this.rows + y];
  }

  /** number of scrollback lines currently stored */
  get scrollbackLength(): number {
    return this.lines.length - this.rows;
  }

  /** index in `lines` of the first row shown in the viewport */
  get viewportTop(): number {
    return this.lines.length - this.rows - this.scrollOffset;
  }

  clampScrollOffset(): void {
    const max = this.scrollbackLength;
    if (this.scrollOffset > max) this.scrollOffset = max;
    if (this.scrollOffset < 0) this.scrollOffset = 0;
  }

  /** Drop the oldest lines once the scrollback exceeds its limit (amortised). */
  trim(force = false): void {
    const excess = this.lines.length - this.rows - this.maxScrollback;
    if (excess <= 0) return;
    if (!force && excess < 256) return;
    this.lines.splice(0, excess);
    this.trimmed += excess;
    this.clampScrollOffset();
  }

  /**
   * Scroll rows [top, bottom] up by one line. Returns true if the line that
   * scrolled off was kept as scrollback.
   */
  scrollUp(top: number, bottom: number, bg: number, keep: boolean): boolean {
    const base = this.ybase;
    const blank = new Line(this.cols, 0, bg);
    if (top === 0 && keep && this.maxScrollback > 0) {
      const first = this.lines[base];
      first.compact();
      if (bottom === this.rows - 1) this.lines.push(blank);
      else this.lines.splice(base + bottom + 1, 0, blank);
      if (this.scrollOffset > 0) this.scrollOffset++;
      this.trim();
      return true;
    }
    this.lines.splice(base + top, 1);
    if (bottom === this.rows - 1) this.lines.push(blank);
    else this.lines.splice(base + bottom, 0, blank);
    return false;
  }

  /** Scroll rows [top, bottom] down by one line. */
  scrollDown(top: number, bottom: number, bg: number): void {
    const base = this.ybase;
    this.lines.splice(base + bottom, 1);
    this.lines.splice(base + top, 0, new Line(this.cols, 0, bg));
  }

  /**
   * Re-wrap soft-wrapped text for a new width. Rows that are not part of a
   * wrapped paragraph and already fit are kept untouched, so the cost is
   * proportional to the amount of wrapped text, not to the scrollback size.
   */
  private reflow(newCols: number): void {
    const old = this.lines;
    const cursorIdx = old.length - this.rows + this.y;
    const out: Line[] = [];
    let newCursorIdx = -1;
    let newCursorX = this.x;
    let i = 0;
    while (i < old.length) {
      // a paragraph = a row plus the wrapped continuation rows that follow it
      let j = i + 1;
      while (j < old.length && old[j].wrapped && old[j].lineAttr === LineAttr.Normal && old[i].lineAttr === LineAttr.Normal) j++;
      const first = old[i];
      const hasCursor = cursorIdx >= i && cursorIdx < j;
      if (j === i + 1 && (first.lineAttr !== LineAttr.Normal || first.trimmedLength() <= newCols)) {
        if (first.cols > newCols) first.compact();
        if (hasCursor) {
          newCursorIdx = out.length;
          newCursorX = Math.min(this.x, newCols - 1);
        }
        out.push(first);
        i = j;
        continue;
      }
      // gather the paragraph's cells as (line, column) references
      const srcLine: Line[] = [];
      const srcCol: number[] = [];
      let cursorOffset = -1;
      for (let k = i; k < j; k++) {
        const l = old[k];
        const len = l.trimmedLength();
        if (k === cursorIdx) cursorOffset = srcLine.length + this.x;
        for (let x = 0; x < len; x++) {
          if ((l.data[x * CELL_SIZE] & WIDTH_MASK) === 0) continue; // spacer follows its wide cell
          srcLine.push(l);
          srcCol.push(x);
        }
        // a cursor inside the row counts cells, so convert its column
        if (k === cursorIdx) {
          let cells = 0;
          for (let x = 0; x < Math.min(this.x, len); x++) if ((l.data[x * CELL_SIZE] & WIDTH_MASK) !== 0) cells++;
          cursorOffset = srcLine.length - countCells(l, len) + cells + Math.max(0, this.x - len);
        }
      }
      let row = new Line(newCols);
      let col = 0;
      const startRow = out.length;
      out.push(row);
      for (let n = 0; n < srcLine.length; n++) {
        const l = srcLine[n];
        const x = srcCol[n];
        const o = x * CELL_SIZE;
        const w = (l.data[o] & WIDTH_MASK) >>> WIDTH_SHIFT;
        if (col + w > newCols) {
          row = new Line(newCols);
          row.wrapped = true;
          out.push(row);
          col = 0;
        }
        if (n === cursorOffset) {
          newCursorIdx = out.length - 1;
          newCursorX = col;
        }
        const ext = l.data[o + 2] & BG_EXT ? (l.ext?.get(x) ?? null) : null;
        const text = l.data[o] & COMBINED ? l.combined?.get(x) : undefined;
        row.setCell(col, l.data[o] & ~COMBINED, l.data[o + 1], l.data[o + 2] & ~BG_EXT, ext, text);
        if (w === 2) row.setCell(col + 1, 0, l.data[o + 1], l.data[o + 2] & ~BG_EXT, ext);
        col += w;
      }
      if (hasCursor && newCursorIdx < 0) {
        // the cursor sat beyond the paragraph's last character
        const extra = Math.max(0, cursorOffset - srcLine.length);
        newCursorIdx = out.length - 1;
        newCursorX = Math.min(col + extra, newCols - 1);
        if (col + extra >= newCols && col >= newCols) {
          const blank = new Line(newCols);
          blank.wrapped = true;
          out.push(blank);
          newCursorIdx = out.length - 1;
          newCursorX = 0;
        }
      }
      void startRow;
      i = j;
    }
    while (out.length < this.rows) out.push(new Line(newCols));
    this.lines = out;
    this.cols = newCols;
    const base = out.length - this.rows;
    if (newCursorIdx < 0) newCursorIdx = out.length - 1;
    // keep the cursor on screen: scroll blank rows below it out of the way
    while (newCursorIdx < out.length - this.rows && out.length > this.rows && out[out.length - 1].trimmedLength() === 0) out.pop();
    this.y = Math.min(Math.max(newCursorIdx - (out.length - this.rows), 0), this.rows - 1);
    this.x = newCursorX;
    void base;
  }

  resize(cols: number, rows: number, keepScrollback: boolean): void {
    if (keepScrollback && cols !== this.cols && this.lines.some((l) => l.wrapped || l.trimmedLength() > cols)) this.reflow(cols);
    let cur = this.rows;
    // --- rows
    while (cur > rows) {
      if (this.y < cur - 1) {
        this.lines.pop();
      } else {
        // content moves up; the top row becomes scrollback (or is dropped)
        if (!keepScrollback || this.maxScrollback === 0) this.lines.shift();
        else this.lines[this.lines.length - cur].compact();
        this.y--;
      }
      cur--;
    }
    while (cur < rows) {
      if (keepScrollback && this.lines.length > cur && this.y === cur - 1) {
        this.y++; // reveal a scrollback line at the top
      } else {
        this.lines.push(new Line(cols));
      }
      cur++;
    }
    this.rows = rows;
    this.cols = cols;
    // --- columns: every visible row has exactly `cols` cells
    const base = this.lines.length - rows;
    for (let i = base; i < this.lines.length; i++) {
      const l = this.lines[i];
      if (l.cols !== cols) l.resize(cols);
    }
    if (this.y < 0) this.y = 0;
    if (this.y >= rows) this.y = rows - 1;
    if (this.x >= cols) this.x = cols - 1;
    this.scrollTop = 0;
    this.scrollBottom = rows - 1;
    this.marginLeft = 0;
    this.marginRight = cols - 1;
    if (this.saved) {
      if (this.saved.x >= cols) this.saved.x = cols - 1;
      if (this.saved.y >= rows) this.saved.y = rows - 1;
    }
    this.trim(true);
    this.clampScrollOffset();
  }

  /** Remove all scrollback lines. */
  clearScrollback(): void {
    const n = this.scrollbackLength;
    if (n > 0) {
      this.lines.splice(0, n);
      this.trimmed += n;
    }
    this.scrollOffset = 0;
  }
}

/** Number of real (non-spacer) cells in the first len columns of a line. */
function countCells(l: Line, len: number): number {
  let n = 0;
  for (let x = 0; x < len; x++) if ((l.data[x * CELL_SIZE] & WIDTH_MASK) !== 0) n++;
  return n;
}
