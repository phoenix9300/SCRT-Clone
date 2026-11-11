// Tools menu: key generation / conversion, agent, host keys, forwards, connection details, vault.
import React, { useCallback, useEffect, useState } from "react";
import { rpc } from "../../rpc";
import { fail, getApp, showDialog, toast } from "../../state/store";
import { confirm } from "../dialogs";
import { features } from "../features";
import { formatSize, pickPath } from "../filePicker";
import { Button, Dialog, Field, Num, Select, Text } from "../kit";
import { registerDataTools } from "./toolsData";
import "./tools.css";

export async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand("copy"); } catch { /* ignore */ }
    ta.remove();
  }
  toast("Copied to clipboard");
}

const sep = () => getApp().info?.pathSep ?? "/";
const baseName = (p: string) => p.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || p;

/** Asks for a passphrase; resolves undefined when cancelled. */
function askPassphrase(file: string, wrong: boolean): Promise<string | undefined> {
  function Body({ close }: { close: (v?: string) => void }) {
    const [v, setV] = useState("");
    return (
      <Dialog title="Passphrase required" onClose={() => close()} onSubmit={() => close(v)}
        footer={<><Button onClick={() => close()}>Cancel</Button><Button primary onClick={() => close(v)}>OK</Button></>}>
        <p style={{ marginTop: 0 }}>Enter the passphrase for <span className="mono">{baseName(file)}</span>.</p>
        {wrong && <p className="error-text">The passphrase was incorrect.</p>}
        <Text value={v} onChange={setV} type="password" autoFocus />
      </Dialog>
    );
  }
  return showDialog<string>((close) => <Body close={close} />);
}

/** Runs a call that may reply {needPassphrase}; retries with what the user types. */
async function withPassphrase<T extends { needPassphrase?: boolean; wrong?: boolean }>(file: string, run: (pass: string) => Promise<T>, first = ""): Promise<T | undefined> {
  let pass = first;
  for (;;) {
    const r = await run(pass);
    if (!r.needPassphrase) return r;
    const p = await askPassphrase(file, !!r.wrong || pass !== "");
    if (p === undefined) return undefined;
    pass = p;
  }
}

function open(render: (close: () => void) => React.ReactNode): void {
  showDialog<void>((close) => <>{render(() => close())}</>);
}

// ------------------------------------------------------------ key generation

const KEY_TYPES: (readonly [string, string])[] = [
  ["rsa-2048", "RSA 2048"], ["rsa-3072", "RSA 3072"], ["rsa-4096", "RSA 4096"],
  ["ecdsa-256", "ECDSA 256"], ["ecdsa-384", "ECDSA 384"], ["ecdsa-521", "ECDSA 521"], ["ed25519-0", "Ed25519"],
];
const FORMATS: (readonly [string, string])[] = [["openssh", "OpenSSH"], ["pem", "PEM (traditional)"], ["ppk", "PuTTY PPK"]];

function Fingerprints({ k }: { k: any }) {
  return (
    <>
      <Field label="SHA-256"><span className="mono">{k.sha256}</span></Field>
      <Field label="MD5"><span className="mono">{k.md5}</span></Field>
    </>
  );
}

function KeyGen({ close }: { close: () => void }) {
  const [kt, setKt] = useState("ed25519-0");
  const [comment, setComment] = useState(() => `hawser@${getApp().info?.hostname ?? "host"}`);
  const [pass, setPass] = useState("");
  const [pass2, setPass2] = useState("");
  const [format, setFormat] = useState("openssh");
  const [path, setPath] = useState("");
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<any>(null);
  const [err, setErr] = useState("");
  const type = kt.split("-")[0], bits = Number(kt.split("-")[1]);
  const dir = getApp().info?.keysDir ?? "";
  const auto = `${dir}${sep()}id_${type}${format === "ppk" ? ".ppk" : ""}`;
  const shown = touched ? path : auto;
  const browse = async () => {
    const p = await pickPath({ mode: "save", title: "Save private key as", startDir: dir, defaultName: baseName(shown) });
    if (p) { setPath(p); setTouched(true); }
  };
  const go = async () => {
    setErr("");
    if (pass !== pass2) return setErr("The passphrases do not match.");
    if (!shown.trim()) return setErr("Choose an output file.");
    let overwrite = false;
    try {
      const st = await rpc.call("fs.stat", { path: shown });
      if (st.exists) {
        if (!(await confirm("Overwrite file?", `${shown} already exists. Replace it with the new key?`, "Overwrite", true))) return;
        overwrite = true;
      }
      setBusy(true);
      setResult(await rpc.call("keys.generate", { type, bits, comment, passphrase: pass, format, path: shown, overwrite }));
    } catch (e) {
      setErr(String((e as Error).message));
    } finally {
      setBusy(false);
    }
  };
  const addAgent = async () => {
    try {
      const r = await withPassphrase(result.path, (p) => rpc.call("agent.add", { path: result.path, passphrase: p }), pass);
      if (r?.added) toast("Key added to the agent");
    } catch (e) { fail(e); }
  };
  if (result) {
    return (
      <Dialog title="Key created" width={640} onClose={close}
        footer={<><Button onClick={addAgent}>Add to Agent</Button><span className="spacer" /><Button primary onClick={close}>Close</Button></>}>
        <Field label="Private key"><span className="mono">{result.path}</span></Field>
        <Field label="Public key file"><span className="mono">{result.publicPath}</span></Field>
        <Field label="Type"><span>{result.algorithm}{result.bits ? ` (${result.bits} bits)` : ""}</span></Field>
        <Fingerprints k={result} />
        <div className="tl-sec">Random art</div>
        <pre className="mono tl-art" aria-label="Random art">{result.randomArt}</pre>
        <div className="tl-sec">Public key</div>
        <pre className="mono tl-pub" aria-label="Public key">{result.openssh}</pre>
        <div style={{ marginTop: 6 }}><Button small onClick={() => copyText(result.openssh)}>Copy</Button></div>
      </Dialog>
    );
  }
  return (
    <Dialog title="Create Public Key" width={560} onClose={close} onSubmit={go}
      footer={<><Button onClick={close}>Cancel</Button><Button primary disabled={busy} onClick={go}>{busy ? "Generating…" : "Generate"}</Button></>}>
      <Field label="Key type"><Select value={kt} onChange={setKt} options={KEY_TYPES} /></Field>
      <Field label="Comment"><Text value={comment} onChange={setComment} /></Field>
      <Field label="Passphrase" hint="Leave empty for an unencrypted key."><Text value={pass} onChange={setPass} type="password" /></Field>
      <Field label="Confirm passphrase"><Text value={pass2} onChange={setPass2} type="password" /></Field>
      <Field label="Format"><Select value={format} onChange={setFormat} options={FORMATS} /></Field>
      <Field label="Output file">
        <Text value={shown} onChange={(v) => { setPath(v); setTouched(true); }} mono /><Button onClick={browse}>Browse…</Button>
      </Field>
      {err && <div className="error-text" role="alert">{err}</div>}
    </Dialog>
  );
}

// ------------------------------------------------------------ key conversion

function KeyConvert({ close }: { close: () => void }) {
  const [file, setFile] = useState("");
  const [info, setInfo] = useState<any>(null);
  const [format, setFormat] = useState("openssh");
  const [ppk, setPpk] = useState("3");
  const [np, setNp] = useState("");
  const [np2, setNp2] = useState("");
  const [out, setOut] = useState("");
  const [err, setErr] = useState("");
  const choose = async () => {
    const p = await pickPath({ mode: "open", title: "Choose a private key", startDir: getApp().info?.keysDir });
    if (!p) return;
    setFile(p); setErr("");
    try {
      const i = await rpc.call("keys.inspect", { path: p });
      setInfo(i);
      setOut(p + ".converted");
    } catch (e) { setInfo(null); setErr(String((e as Error).message)); }
  };
  const go = async () => {
    setErr("");
    if (!file) return setErr("Choose a key file first.");
    if (np !== np2) return setErr("The new passphrases do not match.");
    if (!out.trim()) return setErr("Choose an output file.");
    try {
      let overwrite = false;
      if ((await rpc.call("fs.stat", { path: out })).exists) {
        if (!(await confirm("Overwrite file?", `${out} already exists. Replace it?`, "Overwrite", true))) return;
        overwrite = true;
      }
      const r = await withPassphrase<any>(file, (p) => rpc.call("keys.convert", { path: file, passphrase: p, format, newPassphrase: np, out, ppkVersion: Number(ppk), overwrite }));
      if (r?.path) { toast("Converted key written to " + r.path); close(); }
    } catch (e) { setErr(String((e as Error).message)); }
  };
  const exportPub = async (kind: "openssh" | "rfc4716") => {
    const text = info?.public?.[kind];
    if (!text) return;
    const p = await pickPath({ mode: "save", title: "Save public key", defaultName: baseName(file) + (kind === "openssh" ? ".pub" : ".rfc.pub") });
    if (!p) return;
    try { await rpc.call("fs.writeText", { path: p, data: text.endsWith("\n") ? text : text + "\n" }); toast("Public key saved"); } catch (e) { fail(e); }
  };
  const pub = info?.public;
  return (
    <Dialog title="Convert Private Key" width={600} onClose={close} onSubmit={go}
      footer={<><Button onClick={close}>Close</Button><Button primary onClick={go} disabled={!file}>Convert</Button></>}>
      <Field label="Key file"><Text value={file} onChange={setFile} mono /><Button onClick={choose}>Browse…</Button></Field>
      {info && (
        <div className="tl-kv" style={{ marginBottom: 10 }}>
          <div>Format</div><div>{info.format ?? "unknown"}</div>
          <div>Type</div><div>{pub?.algorithm ?? info.keyType ?? "unknown"}{info.bits ? ` (${info.bits} bits)` : ""}</div>
          <div>Encrypted</div><div>{info.encrypted ? "yes" : "no"}</div>
          <div>Comment</div><div>{info.comment || pub?.comment || "(none)"}</div>
          {pub && <><div>SHA-256</div><div className="mono">{pub.sha256}</div></>}
          {info.inspectError && <><div>Note</div><div className="error-text">{info.inspectError}</div></>}
        </div>
      )}
      <Field label="Target format"><Select value={format} onChange={setFormat} options={FORMATS} /></Field>
      {format === "ppk" && <Field label="PPK version"><Select value={ppk} onChange={setPpk} options={[["3", "Version 3 (current)"], ["2", "Version 2 (older tools)"]]} /></Field>}
      <Field label="New passphrase" hint="Empty = unencrypted output."><Text value={np} onChange={setNp} type="password" /></Field>
      <Field label="Confirm passphrase"><Text value={np2} onChange={setNp2} type="password" /></Field>
      <Field label="Output file"><Text value={out} onChange={setOut} mono /><Button onClick={async () => { const p = await pickPath({ mode: "save", title: "Save converted key", defaultName: baseName(out || file) }); if (p) setOut(p); }}>Browse…</Button></Field>
      {pub && (
        <div className="tl-bar">
          <span className="dim">Export public key:</span>
          <Button small onClick={() => copyText(pub.openssh)}>Copy OpenSSH</Button>
          <Button small onClick={() => exportPub("openssh")}>Save OpenSSH…</Button>
          <Button small onClick={() => exportPub("rfc4716")}>Save RFC 4716…</Button>
        </div>
      )}
      {err && <div className="error-text" role="alert">{err}</div>}
    </Dialog>
  );
}

// ------------------------------------------------------------ agent

function AgentKeys({ close }: { close: () => void }) {
  const [list, setList] = useState<any[]>([]);
  const [sel, setSel] = useState(-1);
  const load = useCallback(() => rpc.call("agent.list").then((l) => setList(l ?? [])).catch(fail), []);
  useEffect(() => { load(); return rpc.on("agent.changed", load); }, [load]);
  const add = async () => {
    const p = await pickPath({ mode: "open", title: "Add key to agent", startDir: getApp().info?.keysDir });
    if (!p) return;
    try {
      const r = await withPassphrase<any>(p, (pw) => rpc.call("agent.add", { path: p, passphrase: pw }));
      if (r?.added) toast("Key added to the agent");
    } catch (e) { fail(e); }
  };
  const remove = () => rpc.call("agent.remove", { openssh: list[sel].openssh }).catch(fail);
  const removeAll = async () => {
    if (await confirm("Remove all keys", "Remove every key from the agent?", "Remove All", true)) rpc.call("agent.remove", { all: true }).catch(fail);
  };
  return (
    <Dialog title="Agent Keys" width={680} onClose={close} footer={<Button primary onClick={close}>Close</Button>}>
      <p className="tl-note">Keys held by Hawser's built-in agent are offered to servers for public-key authentication and are forgotten when Hawser exits.</p>
      <div className="tl-bar">
        <Button onClick={add}>Add…</Button><Button disabled={sel < 0} onClick={remove}>Remove</Button>
        <Button danger disabled={!list.length} onClick={removeAll}>Remove All</Button>
      </div>
      <div className="tl-scroll">
        <table className="tl-table" aria-label="Agent keys">
          <thead><tr><th style={{ width: "26%" }}>Comment</th><th style={{ width: "20%" }}>Type</th><th>SHA-256</th></tr></thead>
          <tbody>
            {list.map((k, i) => (
              <tr key={k.sha256} tabIndex={0} aria-selected={i === sel} onClick={() => setSel(i)} onKeyDown={(e) => { if (e.key === " " || e.key === "Enter") { e.preventDefault(); setSel(i); } }}
                style={i === sel ? { background: "var(--accent-soft)" } : undefined}>
                <td title={k.comment}>{k.comment || "(none)"}</td><td>{k.algorithm}{k.bits ? ` ${k.bits}` : ""}</td><td className="mono" title={k.sha256}>{k.sha256}</td>
              </tr>
            ))}
            {!list.length && <tr><td colSpan={3} className="dim">The agent holds no keys.</td></tr>}
          </tbody>
        </table>
      </div>
    </Dialog>
  );
}

// ------------------------------------------------------------ host keys

function HostKeys({ close }: { close: () => void }) {
  const [list, setList] = useState<any[]>([]);
  const [q, setQ] = useState("");
  const [sel, setSel] = useState("");
  const load = useCallback(() => rpc.call("hostkeys.list").then((l) => setList(l ?? [])).catch(fail), []);
  useEffect(() => { load(); }, [load]);
  const key = (e: any) => e.hosts + "\u0000" + e.type;
  const shown = list.filter((e) => !q || (e.hosts + " " + e.algorithm + " " + e.sha256).toLowerCase().includes(q.toLowerCase()));
  const cur = list.find((e) => key(e) === sel);
  const remove = async () => {
    if (!cur || !(await confirm("Remove host key", `Forget the ${cur.algorithm} key for ${cur.hosts}?`, "Remove", true))) return;
    try { await rpc.call("hostkeys.remove", { hosts: cur.hosts, type: cur.type }); setSel(""); load(); } catch (e) { fail(e); }
  };
  return (
    <Dialog title="Host Key Database" width={760} onClose={close} footer={<Button primary onClick={close}>Close</Button>}>
      <div className="tl-bar">
        <input className="input grow" placeholder="Filter hosts…" aria-label="Filter" value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
        <Button disabled={!cur} onClick={remove}>Remove</Button>
      </div>
      <div className="tl-scroll">
        <table className="tl-table" aria-label="Host keys">
          <thead><tr><th style={{ width: "30%" }}>Host(s)</th><th style={{ width: "16%" }}>Algorithm</th><th className="num" style={{ width: 60 }}>Bits</th><th>SHA-256</th></tr></thead>
          <tbody>
            {shown.map((e) => (
              <tr key={key(e)} tabIndex={0} onClick={() => setSel(key(e))} onKeyDown={(ev) => { if (ev.key === " " || ev.key === "Enter") { ev.preventDefault(); setSel(key(e)); } }}
                style={key(e) === sel ? { background: "var(--accent-soft)" } : undefined}>
                <td title={e.hosts}>{e.hashed ? "(hashed) " : ""}{e.hosts}</td><td>{e.algorithm}</td><td className="num">{e.bits || ""}</td><td className="mono" title={e.sha256}>{e.sha256}</td>
              </tr>
            ))}
            {!shown.length && <tr><td colSpan={4} className="dim">No host keys.</td></tr>}
          </tbody>
        </table>
      </div>
    </Dialog>
  );
}

// ------------------------------------------------------------ forwards

function Forwards({ conn, close }: { conn: number; close: () => void }) {
  const [list, setList] = useState<any[]>([]);
  const [sel, setSel] = useState("");
  const [f, setF] = useState({ name: "", type: "local", bindAddr: "localhost", bindPort: 0, destHost: "", destPort: 0 });
  const [err, setErr] = useState("");
  const load = useCallback(() => rpc.call("conn.forwards", { id: conn }).then((l) => setList(l ?? [])).catch(() => {}), [conn]);
  useEffect(() => { load(); const t = setInterval(load, 2000); return () => clearInterval(t); }, [load]);
  const set = (p: Partial<typeof f>) => setF({ ...f, ...p });
  const add = async () => {
    setErr("");
    if (!f.bindPort) return setErr("Enter a bind port.");
    if (f.type !== "dynamic" && (!f.destHost.trim() || !f.destPort)) return setErr("Enter the destination host and port.");
    const name = f.name.trim() || `${f.type}-${f.bindPort}`;
    try { await rpc.call("conn.forward.add", { id: conn, spec: { ...f, name, enabled: true } }); setF({ ...f, name: "", bindPort: 0, destPort: 0 }); load(); } catch (e) { setErr(String((e as Error).message)); }
  };
  const remove = async () => { try { await rpc.call("conn.forward.remove", { id: conn, name: sel }); setSel(""); load(); } catch (e) { fail(e); } };
  return (
    <Dialog title="Port Forwarding" width={780} onClose={close} onSubmit={add} footer={<Button primary onClick={close}>Close</Button>}>
      <div className="tl-scroll" style={{ maxHeight: 200 }}>
        <table className="tl-table" aria-label="Port forwards">
          <thead><tr><th style={{ width: "16%" }}>Name</th><th style={{ width: 70 }}>Type</th><th style={{ width: "22%" }}>Listen</th><th>Destination</th><th style={{ width: 90 }}>State</th><th className="num" style={{ width: 54 }}>Active</th><th className="num" style={{ width: 54 }}>Total</th></tr></thead>
          <tbody>
            {list.map((s) => (
              <tr key={s.spec.name} tabIndex={0} onClick={() => setSel(s.spec.name)} onKeyDown={(e) => { if (e.key === " " || e.key === "Enter") { e.preventDefault(); setSel(s.spec.name); } }}
                style={s.spec.name === sel ? { background: "var(--accent-soft)" } : undefined}>
                <td>{s.spec.name}</td><td>{s.spec.type}</td><td className="mono">{s.address || `${s.spec.bindAddr}:${s.spec.bindPort}`}</td>
                <td className="mono">{s.spec.type === "dynamic" ? "SOCKS" : `${s.spec.destHost}:${s.spec.destPort}`}</td>
                <td className={s.error ? "error-text" : ""} title={s.error}>{s.error ? "error: " + s.error : s.listening ? "listening" : "stopped"}</td>
                <td className="num">{s.active}</td><td className="num">{s.total}</td>
              </tr>
            ))}
            {!list.length && <tr><td colSpan={7} className="dim">No port forwards on this connection.</td></tr>}
          </tbody>
        </table>
      </div>
      <div className="tl-bar" style={{ marginTop: 8 }}><Button disabled={!sel} onClick={remove}>Remove</Button></div>
      <div className="tl-sec">Add a forward</div>
      <div className="tl-form-row">
        <input className="input" style={{ width: 120 }} placeholder="Name" aria-label="Name" value={f.name} onChange={(e) => set({ name: e.target.value })} />
        <Select value={f.type} onChange={(type) => set({ type })} width={170} options={[["local", "Local (-L)"], ["remote", "Remote (-R)"], ["dynamic", "Dynamic SOCKS (-D)"]]} />
        <input className="input" style={{ width: 130 }} placeholder="Bind address" aria-label="Bind address" value={f.bindAddr} onChange={(e) => set({ bindAddr: e.target.value })} />
        <Num value={f.bindPort} onChange={(bindPort) => set({ bindPort })} min={0} max={65535} width={80} />
      </div>
      {f.type !== "dynamic" && (
        <div className="tl-form-row">
          <input className="input" style={{ width: 200 }} placeholder="Destination host" aria-label="Destination host" value={f.destHost} onChange={(e) => set({ destHost: e.target.value })} />
          <Num value={f.destPort} onChange={(destPort) => set({ destPort })} min={0} max={65535} width={80} />
          <Button primary onClick={add}>Add</Button>
        </div>
      )}
      {f.type === "dynamic" && <Button primary onClick={add}>Add</Button>}
      {err && <div className="error-text" role="alert" style={{ marginTop: 6 }}>{err}</div>}
    </Dialog>
  );
}

// ------------------------------------------------------------ connection info

const LABELS: Record<string, string> = {
  kex: "Key exchange", cipher: "Cipher", mac: "MAC", compression: "Compression", hostKey: "Host key", hostkey: "Host key", serverVersion: "Server version",
  clientVersion: "Client version", user: "User", auth: "Authentication", tls: "TLS", sessionId: "Session ID",
};
const label = (k: string) => LABELS[k] ?? k.replace(/[_-]+/g, " ").replace(/([a-z])([A-Z])/g, "$1 $2").replace(/^./, (c) => c.toUpperCase());

function duration(ms: number): string {
  let s = Math.max(0, Math.floor(ms / 1000));
  const d = Math.floor(s / 86400); s %= 86400;
  const h = Math.floor(s / 3600); s %= 3600;
  const m = Math.floor(s / 60); s %= 60;
  return (d ? d + "d " : "") + (h || d ? h + "h " : "") + (m || h || d ? m + "m " : "") + s + "s";
}

function ConnInfo({ conn, close }: { conn: number; close: () => void }) {
  const [info, setInfo] = useState<any>(null);
  const [err, setErr] = useState("");
  const [, tick] = useState(0);
  useEffect(() => {
    let live = true;
    const load = () => rpc.call("conn.info", { id: conn }).then((i) => live && setInfo(i)).catch((e) => live && setErr(String(e.message)));
    load();
    const t = setInterval(() => { load(); tick((n) => n + 1); }, 2000);
    return () => { live = false; clearInterval(t); };
  }, [conn]);
  const rows: [string, string][] = info ? [
    ["Session", info.session || info.title], ["State", info.state + (info.message ? ` (${info.message})` : "")], ["Target", info.target], ["Protocol", info.protocol],
    ["Connected for", info.connectedAt ? duration(Date.now() - info.connectedAt) : "not connected"],
    ["Data received", formatSize(info.bytesIn)], ["Data sent", formatSize(info.bytesOut)], ["Terminal size", `${info.cols} × ${info.rows}`],
    ["Log file", info.logging ? info.logFile : "not logging"],
    ...Object.entries(info.details ?? {}).sort().map(([k, v]) => [label(k), String(v)] as [string, string]),
  ] : [];
  return (
    <Dialog title="Connection Details" width={560} onClose={close} footer={<Button primary onClick={close}>Close</Button>}>
      {err && <div className="error-text">{err}</div>}
      <div className="tl-kv" role="table" aria-label="Connection details">
        {rows.map(([k, v]) => <React.Fragment key={k}><div>{k}</div><div>{v}</div></React.Fragment>)}
      </div>
    </Dialog>
  );
}

// ------------------------------------------------------------ vault

function Vault({ close }: { close: () => void }) {
  const [st, setSt] = useState<any>(null);
  const [pass, setPass] = useState("");
  const [pass2, setPass2] = useState("");
  const [err, setErr] = useState("");
  const load = useCallback(() => rpc.call("vault.status").then(setSt).catch(fail), []);
  useEffect(() => { load(); return rpc.on("vault.changed", setSt); }, [load]);
  const act = async (fn: () => Promise<any>, ok?: string) => {
    setErr("");
    try { setSt(await fn()); setPass(""); setPass2(""); if (ok) toast(ok); } catch (e) { setErr(String((e as Error).message)); }
  };
  const setPp = () => {
    if (!pass) return setErr("Enter a passphrase (use Remove Passphrase to clear it).");
    if (pass !== pass2) return setErr("The passphrases do not match.");
    act(() => rpc.call("vault.setPassphrase", { passphrase: pass }), "Master passphrase set");
  };
  const removePp = async () => {
    if (await confirm("Remove master passphrase", "Stored passwords will be protected only by a key kept on this computer. Continue?", "Remove", true))
      act(() => rpc.call("vault.setPassphrase", { passphrase: "" }), "Master passphrase removed");
  };
  return (
    <Dialog title="Credential Vault" width={520} onClose={close} footer={<Button primary onClick={close}>Close</Button>}>
      <p className="tl-note">The vault stores the passwords and key passphrases you chose to save, encrypted on disk. With a master passphrase the vault stays locked until you unlock it, so saved credentials cannot be read by anyone with access to your files.</p>
      {st && <>
        <Field label="Status"><span className={"tl-status " + (st.locked ? "bad" : "on")}>{st.locked ? "Locked" : "Unlocked"} · {st.count} saved {st.count === 1 ? "entry" : "entries"} · {st.hasPassphrase ? "master passphrase set" : "no master passphrase"}</span></Field>
        {st.locked ? (
          <Field label="Passphrase"><Text value={pass} onChange={setPass} type="password" autoFocus /><Button primary onClick={() => act(() => rpc.call("vault.unlock", { passphrase: pass }))}>Unlock</Button></Field>
        ) : <>
          <Field label={st.hasPassphrase ? "New passphrase" : "Passphrase"}><Text value={pass} onChange={setPass} type="password" /></Field>
          <Field label="Confirm"><Text value={pass2} onChange={setPass2} type="password" /></Field>
          <div className="tl-bar">
            <Button primary onClick={setPp}>{st.hasPassphrase ? "Change Passphrase" : "Set Passphrase"}</Button>
            {st.hasPassphrase && <Button onClick={() => act(() => rpc.call("vault.lock"))}>Lock Now</Button>}
            {st.hasPassphrase && <Button danger onClick={removePp}>Remove Passphrase</Button>}
          </div>
        </>}
        {err && <div className="error-text" role="alert">{err}</div>}
      </>}
    </Dialog>
  );
}

// ------------------------------------------------------------ registration

features.keygen = () => open((c) => <KeyGen close={c} />);
features.keyConvert = () => open((c) => <KeyConvert close={c} />);
features.agentKeys = () => open((c) => <AgentKeys close={c} />);
features.hostKeys = () => open((c) => <HostKeys close={c} />);
features.forwards = (a?: { conn?: number }) => (a?.conn ? open((c) => <Forwards conn={a.conn!} close={c} />) : toast("No active connection."));
features.connInfo = (a?: { conn?: number }) => (a?.conn ? open((c) => <ConnInfo conn={a.conn!} close={c} />) : toast("No active connection."));
features.vault = () => open((c) => <Vault close={c} />);
registerDataTools();
