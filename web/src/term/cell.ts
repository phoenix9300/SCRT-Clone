// Cell storage layout shared by the emulator core and the renderer.
//
// Every cell occupies CELL_SIZE (3) uint32 words inside a line's Uint32Array:
//
//   word 0  content   bits 0-20  unicode code point (0 = empty cell)
//                     bit  21    COMBINED: cell text is in line.combined
//                     bits 22-23 display width (0 = spacer after a wide char)
//   word 1  fg        bits 0-23  colour value (palette index or 0xRRGGBB)
//                     bits 24-25 colour mode (CM_*)
//                     bits 26-31 FG_* attribute flags
//   word 2  bg        bits 0-23  colour value
//                     bits 24-25 colour mode
//                     bits 26-31 BG_* attribute flags

export const CELL_SIZE = 3;

export const CODE_MASK = 0x1fffff;
export const COMBINED = 0x200000;
export const WIDTH_SHIFT = 22;
export const WIDTH_MASK = 0xc00000;
export const WIDTH_1 = 1 << WIDTH_SHIFT;
export const WIDTH_2 = 2 << WIDTH_SHIFT;

export const COLOR_MASK = 0xffffff;
export const CM_MASK = 0x3000000;
export const CM_DEFAULT = 0;
export const CM_PALETTE = 0x1000000;
export const CM_RGB = 0x2000000;
/** colour mode + value, i.e. everything that is not an attribute flag */
export const COLOR_BITS = 0x3ffffff;

export const FG_INVERSE = 0x4000000;
export const FG_BOLD = 0x8000000;
export const FG_UNDERLINE = 0x10000000;
export const FG_BLINK = 0x20000000;
export const FG_INVISIBLE = 0x40000000;
export const FG_STRIKE = 0x80000000;

export const BG_ITALIC = 0x4000000;
export const BG_DIM = 0x8000000;
export const BG_EXT = 0x10000000;
export const BG_PROTECTED = 0x20000000;
export const BG_OVERLINE = 0x40000000;

/** An empty (never written) cell with default colours. */
export const EMPTY_CONTENT = WIDTH_1;

export const enum UnderlineStyle {
  None = 0,
  Single = 1,
  Double = 2,
  Curly = 3,
  Dotted = 4,
  Dashed = 5,
}

/**
 * Attributes that do not fit into the two colour words. Instances are treated
 * as immutable so they can be shared between cells.
 */
export class ExtAttr {
  constructor(
    public readonly underlineStyle: UnderlineStyle = UnderlineStyle.None,
    /** colour mode + value (COLOR_BITS layout) or -1 for "same as text" */
    public readonly underlineColor: number = -1,
    /** hyperlink id (see Terminal.links), 0 = none */
    public readonly link: number = 0,
  ) {}

  isEmpty(): boolean {
    return (
      (this.underlineStyle === UnderlineStyle.None || this.underlineStyle === UnderlineStyle.Single) &&
      this.underlineColor === -1 &&
      this.link === 0
    );
  }

  with(patch: Partial<{ underlineStyle: UnderlineStyle; underlineColor: number; link: number }>): ExtAttr {
    return new ExtAttr(
      patch.underlineStyle ?? this.underlineStyle,
      patch.underlineColor ?? this.underlineColor,
      patch.link ?? this.link,
    );
  }
}

export const enum LineAttr {
  Normal = 0,
  DoubleWidth = 1,
  DoubleHeightTop = 2,
  DoubleHeightBottom = 3,
}

/** A single row of cells. */
export class Line {
  data: Uint32Array;
  /** true when this row is the soft-wrapped continuation of the previous row */
  wrapped = false;
  lineAttr: LineAttr = LineAttr.Normal;
  /** grapheme clusters that need more than one code point, keyed by column */
  combined: Map<number, string> | null = null;
  /** extended attributes keyed by column (cells flagged BG_EXT) */
  ext: Map<number, ExtAttr> | null = null;

  /** `reuse` is an optional array of the right size to recycle instead of allocating. */
  constructor(cols: number, fg = 0, bg = 0, reuse?: Uint32Array | null) {
    this.data = reuse && reuse.length === cols * CELL_SIZE ? reuse : new Uint32Array(cols * CELL_SIZE);
    this.fill(0, cols, fg, bg);
  }

  get cols(): number {
    return this.data.length / CELL_SIZE;
  }

  /** Fill [start, end) with empty cells carrying the given colour words. */
  fill(start: number, end: number, fg = 0, bg = 0): void {
    const d = this.data;
    const n = this.cols;
    if (start < 0) start = 0;
    if (end > n) end = n;
    if (start >= end) return;
    // erased cells keep only the background colour (BCE), never attributes
    const f = 0;
    const b = bg & COLOR_BITS;
    void fg;
    // do not leave half of a wide character behind at either edge
    if (start > 0 && (d[start * CELL_SIZE] & WIDTH_MASK) === 0) {
      const p = (start - 1) * CELL_SIZE;
      if (d[p] & COMBINED) this.combined?.delete(start - 1);
      d[p] = EMPTY_CONTENT;
    }
    if (end < n && (d[end * CELL_SIZE] & WIDTH_MASK) === 0) d[end * CELL_SIZE] = EMPTY_CONTENT;
    for (let i = start * CELL_SIZE, e = end * CELL_SIZE; i < e; i += CELL_SIZE) {
      d[i] = EMPTY_CONTENT;
      d[i + 1] = f;
      d[i + 2] = b;
    }
    this.dropExtras(start, end);
  }

  /** Remove side-table entries in [start, end). */
  private dropExtras(start: number, end: number): void {
    if (this.combined) {
      if (start === 0 && end >= this.cols) this.combined = null;
      else for (const k of this.combined.keys()) if (k >= start && k < end) this.combined.delete(k);
    }
    if (this.ext) {
      if (start === 0 && end >= this.cols) this.ext = null;
      else for (const k of this.ext.keys()) if (k >= start && k < end) this.ext.delete(k);
    }
  }

  setCell(x: number, content: number, fg: number, bg: number, ext: ExtAttr | null, text?: string): void {
    const i = x * CELL_SIZE;
    const d = this.data;
    if (i < 0 || i >= d.length) return;
    if (d[i] & COMBINED) this.combined?.delete(x);
    if (d[i + 2] & BG_EXT) this.ext?.delete(x);
    if (text !== undefined) {
      content |= COMBINED;
      (this.combined ??= new Map()).set(x, text);
    }
    if (ext) {
      bg |= BG_EXT;
      (this.ext ??= new Map()).set(x, ext);
    } else {
      bg &= ~BG_EXT;
    }
    d[i] = content;
    d[i + 1] = fg;
    d[i + 2] = bg;
  }

  /** Append a combining code point to the cell at x. */
  addCombining(x: number, code: number): void {
    const i = x * CELL_SIZE;
    const d = this.data;
    if (i < 0 || i >= d.length) return;
    const c = d[i];
    if ((c & CODE_MASK) === 0 && !(c & COMBINED)) return;
    const prev = c & COMBINED ? (this.combined?.get(x) ?? "") : String.fromCodePoint(c & CODE_MASK);
    if (prev.length > 32) return; // guard against zalgo floods
    (this.combined ??= new Map()).set(x, prev + String.fromCodePoint(code));
    d[i] = c | COMBINED;
  }

  code(x: number): number {
    return this.data[x * CELL_SIZE] & CODE_MASK;
  }

  width(x: number): number {
    return (this.data[x * CELL_SIZE] & WIDTH_MASK) >>> WIDTH_SHIFT;
  }

  /** Text of a single cell ("" for empty cells and wide-char spacers). */
  cellText(x: number): string {
    const c = this.data[x * CELL_SIZE];
    if (c === undefined) return "";
    if (c & COMBINED) return this.combined?.get(x) ?? "";
    const code = c & CODE_MASK;
    return code ? String.fromCodePoint(code) : "";
  }

  /** Copy cells [srcStart, srcEnd) of `src` to this line at `dst`. */
  copyFrom(src: Line, srcStart: number, srcEnd: number, dst: number): void {
    const n = Math.min(srcEnd - srcStart, this.cols - dst, src.cols - srcStart);
    if (n <= 0) return;
    const hadExtras = this.combined || this.ext || src.combined || src.ext;
    let srcCombined: [number, string][] | null = null;
    let srcExt: [number, ExtAttr][] | null = null;
    if (hadExtras) {
      // snapshot source extras first (src may be === this)
      if (src.combined) {
        srcCombined = [];
        for (const [k, v] of src.combined) if (k >= srcStart && k < srcStart + n) srcCombined.push([k, v]);
      }
      if (src.ext) {
        srcExt = [];
        for (const [k, v] of src.ext) if (k >= srcStart && k < srcStart + n) srcExt.push([k, v]);
      }
    }
    if (src === this) {
      this.data.copyWithin(dst * CELL_SIZE, srcStart * CELL_SIZE, (srcStart + n) * CELL_SIZE);
    } else {
      this.data.set(src.data.subarray(srcStart * CELL_SIZE, (srcStart + n) * CELL_SIZE), dst * CELL_SIZE);
    }
    if (hadExtras) {
      this.dropExtras(dst, dst + n);
      if (srcCombined && srcCombined.length) {
        this.combined ??= new Map();
        for (const [k, v] of srcCombined) this.combined.set(k - srcStart + dst, v);
      }
      if (srcExt && srcExt.length) {
        this.ext ??= new Map();
        for (const [k, v] of srcExt) this.ext.set(k - srcStart + dst, v);
      }
    }
  }

  /** Insert n blank cells at x, shifting cells in [x, right] to the right. */
  insertCells(x: number, n: number, right: number, bg: number): void {
    const width = right - x + 1;
    if (n > width) n = width;
    if (n <= 0) return;
    if (width - n > 0) this.copyFrom(this, x, x + width - n, x + n);
    this.fill(x, x + n, 0, bg);
    this.fixWide(right);
  }

  /** Delete n cells at x, shifting cells in (x, right] left and blank-filling. */
  deleteCells(x: number, n: number, right: number, bg: number): void {
    const width = right - x + 1;
    if (n > width) n = width;
    if (n <= 0) return;
    if (width - n > 0) this.copyFrom(this, x + n, x + width, x);
    this.fill(right + 1 - n, right + 1, 0, bg);
    this.fixWide(x);
  }

  /** Blank out half-characters left behind after cell moves around column x. */
  fixWide(x: number): void {
    const d = this.data;
    const n = this.cols;
    if (x < 0 || x >= n) return;
    const i = x * CELL_SIZE;
    const w = (d[i] & WIDTH_MASK) >>> WIDTH_SHIFT;
    if (w === 0) {
      // spacer whose leading half is gone?
      if (x === 0 || (d[i - CELL_SIZE] & WIDTH_MASK) !== WIDTH_2) {
        d[i] = EMPTY_CONTENT;
      }
    } else if (w === 2) {
      if (x === n - 1 || (d[i + CELL_SIZE] & WIDTH_MASK) !== 0) {
        if (d[i] & COMBINED) this.combined?.delete(x);
        d[i] = EMPTY_CONTENT;
      }
    }
  }

  /** Change the number of columns, padding with empty cells. */
  resize(cols: number, bg = 0): void {
    const old = this.cols;
    if (cols === old) return;
    const nd = new Uint32Array(cols * CELL_SIZE);
    if (cols < old) {
      nd.set(this.data.subarray(0, cols * CELL_SIZE));
      this.data = nd;
      if (this.combined) for (const k of this.combined.keys()) if (k >= cols) this.combined.delete(k);
      if (this.ext) for (const k of this.ext.keys()) if (k >= cols) this.ext.delete(k);
      this.fixWide(cols - 1);
    } else {
      nd.set(this.data);
      this.data = nd;
      const b = bg & COLOR_BITS;
      for (let i = old * CELL_SIZE; i < nd.length; i += CELL_SIZE) {
        nd[i] = EMPTY_CONTENT;
        nd[i + 2] = b;
      }
    }
  }

  /** Index one past the last cell that holds visible content or colour. */
  trimmedLength(): number {
    const d = this.data;
    for (let i = d.length - CELL_SIZE; i >= 0; i -= CELL_SIZE) {
      if ((d[i] & (CODE_MASK | COMBINED)) !== 0 || d[i + 2] !== 0 || (d[i] & WIDTH_MASK) === 0) {
        return i / CELL_SIZE + 1;
      }
    }
    return 0;
  }

  /** Index one past the last cell that holds a character. */
  textLength(): number {
    const d = this.data;
    for (let i = d.length - CELL_SIZE; i >= 0; i -= CELL_SIZE) {
      if ((d[i] & (CODE_MASK | COMBINED)) !== 0) return i / CELL_SIZE + 1;
    }
    return 0;
  }

  /**
   * Shrink storage to the used part (called when a line enters scrollback).
   * With an arena the cells move into shared storage and the full-width
   * array is returned so the caller can recycle it for the next blank row.
   */
  compact(arena?: Arena): Uint32Array | null {
    const n = this.trimmedLength();
    if (n >= this.cols) return null;
    const full = this.data;
    if (!arena) {
      this.data = full.slice(0, n * CELL_SIZE);
      return null;
    }
    const small = arena.alloc(n * CELL_SIZE);
    small.set(full.subarray(0, n * CELL_SIZE));
    this.data = small;
    return full;
  }

  /** Plain text of cells [start, end); empty cells become spaces. */
  text(start = 0, end = this.cols, trimRight = false): string {
    const n = this.cols;
    if (end > n) end = n;
    if (trimRight) {
      const tl = this.textLength();
      if (end > tl) end = tl;
    }
    let s = "";
    const d = this.data;
    for (let x = start; x < end; x++) {
      const c = d[x * CELL_SIZE];
      if ((c & WIDTH_MASK) === 0) continue;
      if (c & COMBINED) s += this.combined?.get(x) ?? " ";
      else {
        const code = c & CODE_MASK;
        s += code ? String.fromCodePoint(code) : " ";
      }
    }
    return s;
  }

  clone(): Line {
    const l = new Line(0);
    l.data = this.data.slice();
    l.wrapped = this.wrapped;
    l.lineAttr = this.lineAttr;
    l.combined = this.combined ? new Map(this.combined) : null;
    l.ext = this.ext ? new Map(this.ext) : null;
    return l;
  }
}

/**
 * Bump allocator for scrollback rows. Allocating one typed array per row is
 * the dominant cost of fast scrolling, so rows share large chunks; a chunk is
 * released by the garbage collector once every row in it has been trimmed.
 */
export class Arena {
  private static readonly CHUNK = 1 << 16;
  private chunk = new Uint32Array(Arena.CHUNK);
  private off = 0;

  alloc(words: number): Uint32Array {
    if (words > Arena.CHUNK >> 3) return new Uint32Array(words);
    if (this.off + words > Arena.CHUNK) {
      this.chunk = new Uint32Array(Arena.CHUNK);
      this.off = 0;
    }
    const v = this.chunk.subarray(this.off, this.off + words);
    this.off += words;
    return v;
  }
}
