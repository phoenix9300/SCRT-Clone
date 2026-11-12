// Records the output of an interactive program that queries the terminal
// (vttest and friends). A reference emulator answers the queries, so the
// recording is what a real session would have received.
//
//   node tests/tools/record-live.mjs OUT.bin COLS ROWS TERM 'command' 'key|key|wait:1|…'
import { spawn } from "node:child_process";
import fs from "node:fs";
import xterm from "@xterm/headless";

const [out, colsS, rowsS, term, cmd, script = ""] = process.argv.slice(2);
const cols = +colsS, rows = +rowsS;
const ref = new xterm.Terminal({ cols, rows, allowProposedApi: true });
const child = spawn("socat", ["-", `SYSTEM:stty sane rows ${rows} cols ${cols}; exec ${cmd},pty,setsid,ctty`], {
  env: { ...process.env, TERM: term, LANG: "C.UTF-8" },
});
const chunks = [];
child.stdout.on("data", (d) => {
  chunks.push(d);
  ref.write(d);
});
ref.onData((s) => child.stdin.write(Buffer.from(s, "latin1")));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
await sleep(800);
for (const step of script.split("|").filter(Boolean)) {
  if (step.startsWith("wait:")) await sleep(parseFloat(step.slice(5)) * 1000);
  else {
    child.stdin.write(step.replace(/\\r/g, "\r").replace(/\\e/g, "\x1b"));
    await sleep(350);
  }
}
await sleep(500);
child.kill();
fs.writeFileSync(out, Buffer.concat(chunks));
console.log(`${out}: ${Buffer.concat(chunks).length} bytes`);
process.exit(0);
