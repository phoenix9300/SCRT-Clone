// Differential test: replay byte streams recorded from real programs through
// our emulator and through @xterm/headless, then compare the screens.
import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import xterm from "@xterm/headless";
import { Terminal } from "../src/term/terminal";
import {
  BG_DIM, BG_ITALIC, CELL_SIZE, CM_MASK, CM_PALETTE, CM_RGB, COLOR_MASK, FG_BLINK, FG_BOLD, FG_INVERSE,
  FG_INVISIBLE, FG_STRIKE, FG_UNDERLINE,
} from "../src/term/cell";

const XTerminal = (xterm as any).Terminal as typeof import("@xterm/headless").Terminal;
const dir = path.join(__dirname, "fixtures");

function writeAll(t: InstanceType<typeof XTerminal>, data: Uint8Array): Promise<void> {
  return new Promise((resolve) => t.write(data, resolve));
}

interface Snapshot {
  text: string[];
  cursor: [number, number];
  attrs: string[];
}

function snapOurs(t: Terminal): Snapshot {
  const text: string[] = [];
  const attrs: string[] = [];
  for (let y = 0; y < t.rows; y++) {
    const line = t.buf.line(y);
    text.push(line.text(0, t.cols, true));
    let a = "";
    const n = line.textLength();
    for (let x = 0; x < n; x++) {
      const fg = line.data[x * CELL_SIZE + 1];
      const bg = line.data[x * CELL_SIZE + 2];
      if (line.width(x) === 0) continue;
      const col = (w: number) =>
        (w & CM_MASK) === CM_PALETTE ? "p" + (w & COLOR_MASK) : (w & CM_MASK) === CM_RGB ? "#" + (w & COLOR_MASK).toString(16) : "d";
      a +=
        col(fg) + "/" + col(bg) +
        (fg & FG_BOLD ? "B" : "") + (bg & BG_DIM ? "D" : "") + (bg & BG_ITALIC ? "I" : "") +
        (fg & FG_UNDERLINE ? "U" : "") + (fg & FG_BLINK ? "K" : "") + (fg & FG_INVERSE ? "R" : "") +
        (fg & FG_INVISIBLE ? "H" : "") + (fg & FG_STRIKE ? "S" : "") + " ";
    }
    attrs.push(a);
  }
  return { text, cursor: [t.buf.x, t.buf.y], attrs };
}

function snapRef(t: InstanceType<typeof XTerminal>): Snapshot {
  const text: string[] = [];
  const attrs: string[] = [];
  const b = t.buffer.active;
  const cell = b.getNullCell();
  for (let y = 0; y < t.rows; y++) {
    const line = b.getLine(b.baseY + y)!;
    const s = line.translateToString(true);
    text.push(s);
    let a = "";
    // number of cells that hold text
    let n = 0;
    for (let x = t.cols - 1; x >= 0; x--) {
      line.getCell(x, cell);
      if (cell.getChars() !== "") {
        n = x + 1;
        break;
      }
    }
    for (let x = 0; x < n; x++) {
      line.getCell(x, cell);
      if (cell.getWidth() === 0) continue;
      const col = (isDef: boolean, isPal: boolean, v: number) => (isDef ? "d" : isPal ? "p" + v : "#" + v.toString(16));
      a +=
        col(cell.isFgDefault(), cell.isFgPalette(), cell.getFgColor()) + "/" +
        col(cell.isBgDefault(), cell.isBgPalette(), cell.getBgColor()) +
        (cell.isBold() ? "B" : "") + (cell.isDim() ? "D" : "") + (cell.isItalic() ? "I" : "") +
        (cell.isUnderline() ? "U" : "") + (cell.isBlink() ? "K" : "") + (cell.isInverse() ? "R" : "") +
        (cell.isInvisible() ? "H" : "") + (cell.isStrikethrough() ? "S" : "") + " ";
    }
    attrs.push(a);
  }
  return { text, cursor: [Math.min(b.cursorX, t.cols - 1), b.cursorY], attrs };
}

/**
 * Known, deliberate divergence: like xterm (and unlike xterm.js) an erase with
 * the cursor in the pending-wrap state clears the cell in the last column.
 * Accept rows where the reference keeps a blank in the last column that we erased.
 */
function normalise(ours: Snapshot, ref: Snapshot, cols: number): void {
  for (let y = 0; y < ref.text.length; y++) {
    const r = ref.text[y];
    if (ours.text[y] !== r && r.length === cols && r[cols - 1] === " " && (ours.text[y] === r.slice(0, cols - 1) || ours.text[y] === r.slice(0, cols - 1).trimEnd())) {
      ref.text[y] = ours.text[y];
      const keep = ours.attrs[y].split(" ").length;
      ref.attrs[y] = ref.attrs[y].split(" ").slice(0, keep - 1).join(" ") + (keep > 1 ? " " : "");
    }
  }
}

const fixtures = fs.existsSync(dir) ? fs.readdirSync(dir).filter((f) => f.endsWith(".bin")).sort() : [];

describe("differential vs xterm headless", () => {
  for (const f of fixtures) {
    const m = /-(\d+)x(\d+)\.bin$/.exec(f)!;
    const cols = parseInt(m[1], 10);
    const rows = parseInt(m[2], 10);
    it(f, async () => {
      const data = new Uint8Array(fs.readFileSync(path.join(dir, f)));
      const ours = new Terminal({ cols, rows, scrollback: 1000 });
      const ref = new XTerminal({ cols, rows, scrollback: 1000, allowProposedApi: true });
      // feed in uneven chunks to exercise sequence splitting, comparing as we go
      let off = 0;
      let step = 0;
      while (off < data.length) {
        const n = 1 + ((step * 7919) % 613);
        const chunk = data.subarray(off, off + n);
        off += n;
        step++;
        ours.write(chunk);
        await writeAll(ref, chunk);
        if (step % 5 === 0 || off >= data.length) {
          const a = snapOurs(ours);
          const b = snapRef(ref);
          normalise(a, b, cols);
          expect(a.text, `text mismatch at byte ${off}`).toEqual(b.text);
          expect(a.cursor, `cursor mismatch at byte ${off}`).toEqual(b.cursor);
          expect(a.attrs, `attribute mismatch at byte ${off}`).toEqual(b.attrs);
        }
      }
      // scrollback must match too
      const sbOurs: string[] = [];
      for (let i = 0; i < ours.normal.scrollbackLength; i++) sbOurs.push(ours.normal.lines[i].text(0, cols, true));
      const sbRef: string[] = [];
      const nb = ref.buffer.normal;
      for (let i = 0; i < nb.baseY; i++) sbRef.push(nb.getLine(i)!.translateToString(true));
      // we trim scrollback lazily, so only the common tail is comparable
      const n = Math.min(sbOurs.length, sbRef.length);
      expect(n).toBeGreaterThanOrEqual(Math.min(sbRef.length, 1000));
      expect(sbOurs.slice(sbOurs.length - n)).toEqual(sbRef.slice(sbRef.length - n));
    });
  }
});
