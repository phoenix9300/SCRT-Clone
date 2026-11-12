// Raw data view: a hex dump of the bytes received from and sent to a session,
// for debugging escape sequences and device protocols.
import { useEffect, useRef, useState } from "react";
import { rpc } from "../../rpc";
import { showDialog } from "../../state/store";
import { features } from "../features";
import { Button, Check, Dialog } from "../kit";

interface Chunk { dir: "in" | "out"; data: Uint8Array; t: number }
const KEEP = 64 * 1024;
const history = new Map<number, { chunks: Chunk[]; size: number }>();
const listeners = new Set<(conn: number) => void>();

rpc.tap((dir, conn, data) => {
  let h = history.get(conn);
  if (!h) history.set(conn, (h = { chunks: [], size: 0 }));
  h.chunks.push({ dir, data: data.slice(), t: Date.now() });
  h.size += data.length;
  while (h.size > KEEP && h.chunks.length > 1) h.size -= h.chunks.shift()!.data.length;
  listeners.forEach((l) => l(conn));
});

rpc.on("conn.closed", (d: { id: number }) => history.delete(d.id));

/** Classic 16-bytes-per-row dump; consecutive chunks of one direction are joined. */
export function hexDump(chunks: Chunk[], showIn: boolean, showOut: boolean): string {
  const out: string[] = [];
  let run: number[] = [];
  let dir: "in" | "out" | null = null;
  const flush = () => {
    if (!run.length || !dir) return;
    out.push(dir === "in" ? "── received ──" : "── sent ──");
    for (let o = 0; o < run.length; o += 16) {
      const row = run.slice(o, o + 16);
      const hex = row.map((b) => b.toString(16).padStart(2, "0"));
      const left = hex.slice(0, 8).join(" ").padEnd(23);
      const right = hex.slice(8).join(" ").padEnd(23);
      const ascii = row.map((b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : ".")).join("");
      out.push(`${o.toString(16).padStart(6, "0")}  ${left}  ${right}  ${ascii}`);
    }
    run = [];
  };
  for (const c of chunks) {
    if ((c.dir === "in" && !showIn) || (c.dir === "out" && !showOut)) continue;
    if (c.dir !== dir) {
      flush();
      dir = c.dir;
    }
    for (const b of c.data) run.push(b);
  }
  flush();
  return out.join("\n");
}

function HexView({ conn, close }: { conn: number; close: () => void }) {
  const [, tick] = useState(0);
  const [paused, setPaused] = useState(false);
  const [showIn, setShowIn] = useState(true);
  const [showOut, setShowOut] = useState(true);
  const frozen = useRef<Chunk[] | null>(null);
  const pre = useRef<HTMLPreElement>(null);
  useEffect(() => {
    let raf = 0;
    const l = (c: number) => {
      if (c !== conn || raf) return;
      raf = requestAnimationFrame(() => { raf = 0; tick((n) => n + 1); });
    };
    listeners.add(l);
    return () => { listeners.delete(l); cancelAnimationFrame(raf); };
  }, [conn]);
  const live = history.get(conn)?.chunks ?? [];
  const chunks = paused ? (frozen.current ??= live.slice()) : ((frozen.current = null), live);
  // show the most recent 64 KiB; the full capture can be copied
  let shown = chunks, size = 0;
  for (let i = chunks.length - 1; i >= 0; i--) {
    size += chunks[i].data.length;
    if (size > 64 * 1024) { shown = chunks.slice(i + 1); break; }
  }
  const text = hexDump(shown, showIn, showOut);
  useEffect(() => { if (!paused && pre.current) pre.current.scrollTop = pre.current.scrollHeight; });
  return (
    <Dialog title="Raw Data (Hex View)" width={760} height={560} flush onClose={close}
      footer={<>
        <Check checked={showIn} onChange={setShowIn} label="Received" /><Check checked={showOut} onChange={setShowOut} label="Sent" />
        <span className="spacer" />
        <Button onClick={() => setPaused(!paused)}>{paused ? "Resume" : "Pause"}</Button>
        <Button onClick={() => { history.delete(conn); frozen.current = null; tick((n) => n + 1); }}>Clear</Button>
        <Button onClick={() => navigator.clipboard?.writeText(hexDump(chunks, showIn, showOut)).catch(() => {})}>Copy</Button>
        <Button primary onClick={close}>Close</Button></>}>
      <pre ref={pre} className="mono hexview" style={{ flex: 1, margin: 0, padding: "10px 14px", overflow: "auto", minHeight: 0, whiteSpace: "pre", lineHeight: 1.45 }}>
        {text || "No data captured yet. Bytes appear here as they are received and sent."}
      </pre>
    </Dialog>
  );
}

features.hexView = (arg: { conn: number }) => void showDialog<void>((close) => <HexView conn={arg.conn} close={() => close()} />);
