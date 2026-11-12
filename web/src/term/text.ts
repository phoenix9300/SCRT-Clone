// Text extraction, selection helpers, search and link detection on the buffer.

import { Buffer } from "./buffer";
import { CELL_SIZE, CODE_MASK, COMBINED, Line, WIDTH_MASK } from "./cell";

export interface Pos {
  /** absolute line number (buffer.trimmed + index into buffer.lines) */
  line: number;
  col: number;
}

export function comparePos(a: Pos, b: Pos): number {
  return a.line !== b.line ? a.line - b.line : a.col - b.col;
}

/** Text between two positions (inclusive). Soft-wrapped rows are joined. */
export function getText(buf: Buffer, a: Pos, b: Pos, block: boolean, cols: number): string {
  if (comparePos(a, b) > 0) [a, b] = [b, a];
  const out: string[] = [];
  const c1 = Math.min(a.col, b.col);
  const c2 = Math.max(a.col, b.col);
  for (let ln = a.line; ln <= b.line; ln++) {
    const line = buf.lines[ln - buf.trimmed];
    if (!line) continue;
    let s: string;
    if (block) s = line.text(c1, c2 + 1, true);
    else {
      const from = ln === a.line ? a.col : 0;
      const to = ln === b.line ? b.col + 1 : cols;
      s = line.text(from, to, true);
    }
    const next = buf.lines[ln + 1 - buf.trimmed];
    const joined = !block && ln < b.line && next?.wrapped;
    out.push(joined ? s : s.replace(/\s+$/, ""));
    if (ln < b.line && !joined) out.push("\n");
  }
  return out.join("");
}

/** Column range of the word at (line, col) using the given delimiter set. */
export function wordAt(line: Line, col: number, delimiters: string): [number, number] {
  const n = line.cols;
  const isDelim = (x: number) => {
    const c = line.data[x * CELL_SIZE];
    if ((c & WIDTH_MASK) === 0) return false; // spacer belongs to its wide char
    const code = c & CODE_MASK;
    if (c & COMBINED) return false;
    return code === 0 || delimiters.includes(String.fromCodePoint(code));
  };
  if (col >= n) return [col, col];
  const d = isDelim(col);
  let a = col;
  let b = col;
  while (a > 0 && isDelim(a - 1) === d) a--;
  while (b < n - 1 && isDelim(b + 1) === d) b++;
  return [a, b];
}

/** Map a string index within line.text() back to a column. */
export function colMap(line: Line, cols: number): number[] {
  const map: number[] = [];
  const n = Math.min(cols, line.cols);
  for (let x = 0; x < n; x++) {
    const c = line.data[x * CELL_SIZE];
    if ((c & WIDTH_MASK) === 0) continue;
    const t = c & COMBINED ? (line.combined?.get(x) ?? " ") : (c & CODE_MASK) > 0xffff ? "xx" : "x";
    for (let i = 0; i < t.length; i++) map.push(x);
  }
  map.push(n);
  return map;
}

export interface SearchOptions {
  caseSensitive: boolean;
  regex: boolean;
  wholeWord?: boolean;
}

export function buildSearch(query: string, o: SearchOptions): RegExp | null {
  if (!query) return null;
  let src = o.regex ? query : query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  if (o.wholeWord) src = `\\b(?:${src})\\b`;
  try {
    return new RegExp(src, o.caseSensitive ? "g" : "gi");
  } catch {
    return null;
  }
}

export interface Match {
  line: number; // absolute
  start: number; // column
  end: number; // exclusive column
}

/** All matches of re on one line. */
export function matchLine(line: Line, absLine: number, cols: number, re: RegExp): Match[] {
  const text = line.text(0, cols, true);
  if (!text) return [];
  const map = colMap(line, cols);
  const out: Match[] = [];
  re.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m[0].length === 0) {
      re.lastIndex++;
      continue;
    }
    const s = map[m.index] ?? 0;
    const e = (map[m.index + m[0].length - 1] ?? s) + 1;
    out.push({ line: absLine, start: s, end: e });
    if (out.length > 500) break;
  }
  return out;
}

/** Find the next match after (or before) a position, wrapping around. */
export function findNext(buf: Buffer, cols: number, re: RegExp, from: Pos, backward: boolean): Match | null {
  const total = buf.lines.length;
  if (total === 0) return null;
  const startIdx = Math.min(Math.max(from.line - buf.trimmed, 0), total - 1);
  for (let step = 0; step <= total; step++) {
    const idx = backward ? (startIdx - step + total * 2) % total : (startIdx + step) % total;
    const ms = matchLine(buf.lines[idx], buf.trimmed + idx, cols, re);
    if (!ms.length) continue;
    if (step === 0) {
      const cand = backward ? ms.filter((m) => m.start < from.col).pop() : ms.find((m) => m.start > from.col);
      if (cand) return cand;
      continue;
    }
    return backward ? ms[ms.length - 1] : ms[0];
  }
  return null;
}

const URL_RE = /\b(?:https?|ftp):\/\/[^\s<>"'`{}|\\^\u0000-\u001f]+[^\s<>"'`{}|\\^.,;:!?)\]\u0000-\u001f]/g;

/** URLs on a line as [startCol, endColExclusive, url]. */
export function findLinks(line: Line, cols: number): [number, number, string][] {
  const text = line.text(0, cols, true);
  if (text.indexOf("://") < 0) return [];
  const map = colMap(line, cols);
  const out: [number, number, string][] = [];
  URL_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = URL_RE.exec(text))) out.push([map[m.index], map[m.index + m[0].length - 1] + 1, m[0]]);
  return out;
}
