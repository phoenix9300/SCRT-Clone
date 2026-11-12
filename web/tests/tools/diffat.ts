// usage: vite-node tests/tools/diffat.ts FILE COLS ROWS OFFSET  — compare both emulators after OFFSET bytes
import fs from "node:fs";
import xterm from "@xterm/headless";
import { Terminal } from "../../src/term/terminal";
const [file, colsS, rowsS, offS] = process.argv.slice(2);
const cols = +colsS, rows = +rowsS;
const data = new Uint8Array(fs.readFileSync(file)).subarray(0, +offS);
const ours = new Terminal({ cols, rows, scrollback: 1000 });
const ref = new (xterm as any).Terminal({ cols, rows, scrollback: 1000, allowProposedApi: true });
await new Promise<void>((r) => ref.write(data, r));
ours.write(data);
for (let y = 0; y < rows; y++) {
  const a = ours.rowText(y);
  const b = ref.buffer.active.getLine(ref.buffer.active.baseY + y).translateToString(true);
  if (a !== b) console.log(y, "ours:", JSON.stringify(a), "\n   ref :", JSON.stringify(b));
}
console.log("cursor ours", ours.buf.x, ours.buf.y, "ref", ref.buffer.active.cursorX, ref.buffer.active.cursorY);
console.log("tail:", JSON.stringify(Buffer.from(data.subarray(Math.max(0, data.length - 260))).toString("latin1")));
// attribute differences (first few)
{
  const b = ref.buffer.active;
  const cell = b.getNullCell();
  let shown = 0;
  for (let y = 0; y < rows && shown < 6; y++) {
    const line = ours.buf.line(y);
    const rl = b.getLine(b.baseY + y);
    for (let x = 0; x < cols && shown < 6; x++) {
      rl.getCell(x, cell);
      const fg = line.data[x * 3 + 1], bg = line.data[x * 3 + 2];
      const o = `${(fg >>> 24) & 3}:${fg & 0xffffff}/${(bg >>> 24) & 3}:${bg & 0xffffff} flags ${(fg >>> 26).toString(2)}`;
      const r = `${cell.isFgDefault() ? 0 : cell.isFgPalette() ? 1 : 2}:${cell.getFgColor() < 0 ? 0 : cell.getFgColor()}/${cell.isBgDefault() ? 0 : cell.isBgPalette() ? 1 : 2}:${cell.getBgColor() < 0 ? 0 : cell.getBgColor()} B${cell.isBold()}U${cell.isUnderline()}K${cell.isBlink()}R${cell.isInverse()}`;
      if (o.split(" ")[0] !== r.split(" ")[0] && cell.getChars() !== "") { console.log(`cell ${y},${x} '${cell.getChars()}' ours ${o} ref ${r}`); shown++; }
    }
  }
}
