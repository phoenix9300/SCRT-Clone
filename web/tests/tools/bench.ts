// Parser/emulator throughput: vite-node tests/tools/bench.ts
import fs from "node:fs";
import { Terminal } from "../../src/term/terminal";
for (const f of (process.env.BENCH ?? "ls-80x24.bin,vim-132x43.bin,htop-100x30.bin").split(",")) {
  const data = new Uint8Array(fs.readFileSync("tests/fixtures/" + f));
  const t = new Terminal({ cols: 132, rows: 43, scrollback: 100000 });
  const reps = Math.ceil(40e6 / data.length);
  const t0 = performance.now();
  for (let i = 0; i < reps; i++) t.write(data);
  const s = (performance.now() - t0) / 1000;
  console.log(f.padEnd(18), ((data.length * reps) / 1e6 / s).toFixed(1), "MB/s", "scrollback", t.normal.scrollbackLength);
}
