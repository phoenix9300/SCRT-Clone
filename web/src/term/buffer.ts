import { Line, ExtAttr } from "./cell";

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

  resize(cols: number, rows: number, keepScrollback: boolean): void {
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
