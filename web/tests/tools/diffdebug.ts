import fs from "node:fs";
import xterm from "@xterm/headless";
import { Terminal } from "../../src/term/terminal";
const XTerminal = (xterm as any).Terminal;
const [file, colsS, rowsS] = process.argv.slice(2);
const cols = +colsS, rows = +rowsS;
const data = new Uint8Array(fs.readFileSync(file));
const ours = new Terminal({ cols, rows, scrollback: 1000 });
const ref = new XTerminal({ cols, rows, scrollback: 1000, allowProposedApi: true });
await new Promise<void>((r) => ref.write(data, r));
ours.write(data);
const a: string[] = [], b: string[] = [];
for (let i = 0; i < ours.normal.lines.length; i++) a.push(ours.normal.lines[i].text(0, cols, true));
const nb = ref.buffer.normal;
for (let i = 0; i < nb.length; i++) b.push(nb.getLine(i).translateToString(true));
console.log("lines ours", a.length, "ref", b.length);
for (let i = 0; i < Math.max(a.length, b.length); i++) {
  if (a[i] !== b[i]) {
    console.log("first diff at", i);
    for (let j = Math.max(0, i - 2); j < i + 4; j++) console.log(j, JSON.stringify(a[j]), "\n ", JSON.stringify(b[j]));
    break;
  }
}
