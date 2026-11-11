// Core dialogs: confirmation, text prompt, engine prompts, Quick Connect, About.
import { useState } from "react";
import { rpc } from "../rpc";
import { connect, fail, getApp, showDialog, ui } from "../state/store";
import { Button, Check, Dialog, Field, Num, Select, Text } from "./kit";

export function confirm(title: string, message: string, ok = "OK", danger = false): Promise<boolean> {
  return showDialog<boolean>((close) => (
    <Dialog title={title} onClose={() => close(false)} onSubmit={() => close(true)}
      footer={<><Button onClick={() => close(false)}>Cancel</Button><Button primary={!danger} danger={danger} onClick={() => close(true)}>{ok}</Button></>}>
      <div style={{ whiteSpace: "pre-wrap", userSelect: "text" }}>{message}</div>
    </Dialog>
  )).then((v) => !!v);
}

export function alertBox(title: string, message: string): Promise<void> {
  return showDialog<void>((close) => (
    <Dialog title={title} onClose={() => close()} onSubmit={() => close()} footer={<Button primary onClick={() => close()}>OK</Button>}>
      <div style={{ whiteSpace: "pre-wrap", userSelect: "text" }}>{message}</div>
    </Dialog>
  )).then(() => {});
}

export function promptText(title: string, label: string, initial = "", opts: { secret?: boolean; ok?: string } = {}): Promise<string | undefined> {
  function Body({ close }: { close: (v?: string) => void }) {
    const [v, setV] = useState(initial);
    return (
      <Dialog title={title} onClose={() => close()} onSubmit={() => close(v)}
        footer={<><Button onClick={() => close()}>Cancel</Button><Button primary onClick={() => close(v)}>{opts.ok ?? "OK"}</Button></>}>
        <div style={{ marginBottom: 8 }}>{label}</div>
        <Text value={v} onChange={setV} type={opts.secret ? "password" : "text"} autoFocus />
      </Dialog>
    );
  }
  return showDialog<string>((close) => <Body close={close} />);
}

// ---------------------------------------------------------- engine prompts

interface PromptReq {
  kind: string; title: string; message: string; fields?: { label: string; secret: boolean; default?: string }[];
  hostKey?: { host: string; algorithm: string; bits: number; sha256: string; md5: string; randomArt?: string; changed: boolean; oldSha256?: string; oldAlgorithm?: string };
  allowSave?: boolean;
}

function EnginePrompt({ req, close }: { req: PromptReq; close: (v?: any) => void }) {
  const [values, setValues] = useState<string[]>((req.fields ?? []).map((f) => f.default ?? ""));
  const [save, setSave] = useState(false);
  const cancel = () => close({ ok: false, choice: "reject" });
  if (req.kind === "hostkey" && req.hostKey) {
    const k = req.hostKey;
    return (
      <Dialog title={req.title} width={560} onClose={cancel}
        footer={<><Button onClick={cancel}>Cancel</Button><span className="spacer" />
          <Button onClick={() => close({ ok: true, choice: "once" })}>Accept Once</Button>
          <Button primary={!k.changed} danger={k.changed} onClick={() => close({ ok: true, choice: "save" })}>Accept &amp; Save</Button></>}>
        <p style={{ marginTop: 0 }} className={k.changed ? "error-text" : ""}>{req.message}</p>
        <Field label="Host"><span className="mono">{k.host}</span></Field>
        <Field label="Key type"><span>{k.algorithm} {k.bits ? `(${k.bits} bits)` : ""}</span></Field>
        <Field label="SHA-256"><span className="mono">{k.sha256}</span></Field>
        <Field label="MD5"><span className="mono">{k.md5}</span></Field>
        {k.changed && <Field label="Previously saved"><span className="mono">{k.oldAlgorithm} {k.oldSha256}</span></Field>}
        {k.randomArt && <pre className="mono" style={{ margin: "8px 0 0 162px", lineHeight: 1.15 }}>{k.randomArt}</pre>}
      </Dialog>
    );
  }
  const submit = () => close({ ok: true, values, save });
  return (
    <Dialog title={req.title} onClose={cancel} onSubmit={submit}
      footer={<><Button onClick={cancel}>Cancel</Button><Button primary onClick={submit}>OK</Button></>}>
      <p style={{ marginTop: 0, whiteSpace: "pre-wrap" }}>{req.message}</p>
      {(req.fields ?? []).map((f, i) => (
        <Field key={i} label={f.label.replace(/:\s*$/, "") || "Response"}>
          <Text value={values[i]} type={f.secret ? "password" : "text"} autoFocus={i === 0}
            onChange={(v) => setValues(values.map((x, j) => (j === i ? v : x)))} />
        </Field>
      ))}
      {req.allowSave && <Check checked={save} onChange={setSave} label={req.kind === "passphrase" ? "Remember this passphrase" : "Save password with the session"} />}
    </Dialog>
  );
}

export function registerEnginePrompts(): void {
  rpc.handle("prompt", (data, signal) => new Promise((resolve) => {
    let closer: ((v?: any) => void) | null = null;
    showDialog<any>((close) => {
      closer = close;
      return <EnginePrompt req={data.prompt} close={close} />;
    }).then((v) => resolve(v ?? { ok: false, choice: "reject" }));
    signal.addEventListener("abort", () => closer?.({ ok: false, choice: "reject" }));
  }));
  ui.confirm = (title, message, ok) => confirm(title, message, ok);
  ui.confirmPaste = (text) => {
    const lines = text.split("\n");
    const preview = lines.slice(0, 12).join("\n") + (lines.length > 12 ? `\n… (${lines.length - 12} more lines)` : "");
    return showDialog<boolean>((close) => (
      <Dialog title="Paste multiple lines?" width={560} onClose={() => close(false)} onSubmit={() => close(true)}
        footer={<><Button onClick={() => close(false)}>Cancel</Button><Button primary onClick={() => close(true)}>Paste {lines.length} lines</Button></>}>
        <pre className="mono" style={{ margin: 0, maxHeight: 260, overflow: "auto", padding: 8, background: "var(--bg)", borderRadius: 5 }}>{preview}</pre>
      </Dialog>
    )).then((v) => !!v);
  };
}

// ------------------------------------------------------------ Quick Connect

export const PROTOCOLS: (readonly [string, string])[] = [
  ["ssh2", "SSH2"], ["telnet", "Telnet"], ["telnet-tls", "Telnet/TLS"], ["rlogin", "Rlogin"], ["raw", "Raw TCP"], ["serial", "Serial"], ["local", "Local Shell"],
];
const PORTS: Record<string, number> = { ssh2: 22, telnet: 23, "telnet-tls": 992, rlogin: 513, raw: 0 };
export const BAUDS = [300, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600];

function QuickConnect({ close, initial }: { close: () => void; initial?: string }) {
  const m = /^(?:([^@]+)@)?(.*)$/.exec(initial ?? "")!;
  const [protocol, setProtocol] = useState("ssh2");
  const [host, setHost] = useState(m[2] ?? "");
  const [port, setPort] = useState(22);
  const [username, setUsername] = useState(m[1] ?? "");
  const [serialPort, setSerialPort] = useState("");
  const [baud, setBaud] = useState("9600");
  const [ports, setPorts] = useState<string[] | null>(null);
  const [shell, setShell] = useState("");
  const [save, setSave] = useState(false);
  const [name, setName] = useState("");
  const net = protocol !== "serial" && protocol !== "local";
  const pick = (p: string) => {
    setProtocol(p);
    if (PORTS[p] !== undefined) setPort(PORTS[p]);
    if (p === "serial" && !ports) rpc.call("serial.ports").then((l) => { setPorts(l); if (l[0]) setSerialPort((s) => s || l[0]); });
  };
  const go = async () => {
    const ov: any = { protocol };
    if (net) Object.assign(ov, { host: host.trim(), port, username: username.trim() });
    else if (protocol === "serial") ov.serial = { port: serialPort, baud: Number(baud) };
    else if (shell.trim()) ov.local = { shell: shell.trim() };
    if (net && !ov.host) return;
    if (save) {
      const path = name.trim() || ov.host || serialPort || "Local Shell";
      try {
        await rpc.call("sessions.save", { path, overrides: ov, mustBeNew: true });
      } catch (e) {
        return fail(e);
      }
      close();
      connect({ session: path });
    } else {
      close();
      connect({ overrides: ov });
    }
  };
  return (
    <Dialog title="Quick Connect" width={470} onClose={close} onSubmit={go}
      footer={<><Button onClick={close}>Cancel</Button><Button primary onClick={go}>Connect</Button></>}>
      <Field label="Protocol"><Select value={protocol} onChange={pick} options={PROTOCOLS} /></Field>
      {net && <>
        <Field label="Hostname"><Text value={host} onChange={setHost} autoFocus placeholder="host name or IP address" /></Field>
        <Field label="Port"><Num value={port} onChange={setPort} min={0} max={65535} /></Field>
        {protocol !== "raw" && <Field label="Username"><Text value={username} onChange={setUsername} placeholder={protocol === "ssh2" ? "asked when empty" : "optional"} /></Field>}
      </>}
      {protocol === "serial" && <>
        <Field label="Port">{ports && ports.length ? <Select value={serialPort} onChange={setSerialPort} options={ports.map((p) => [p, p] as const)} /> : <Text value={serialPort} onChange={setSerialPort} placeholder="/dev/ttyUSB0 or COM3" />}</Field>
        <Field label="Baud rate"><Select value={baud} onChange={setBaud} options={BAUDS.map((b) => [String(b), String(b)] as const)} /></Field>
      </>}
      {protocol === "local" && <Field label="Shell" hint={"Default: " + (getApp().info?.defaultShell ?? "")}><Text value={shell} onChange={setShell} placeholder="default shell" /></Field>}
      <div style={{ height: 6 }} />
      <Check checked={save} onChange={setSave} label="Save session" />
      {save && <Field label="Session name"><Text value={name} onChange={setName} placeholder={host || "name (use / for folders)"} /></Field>}
    </Dialog>
  );
}

export function openQuickConnect(initial?: string): void {
  showDialog<void>((close) => <QuickConnect close={() => close()} initial={initial} />);
}

export function openAbout(): void {
  const info = getApp().info ?? {};
  showDialog<void>((close) => (
    <Dialog title="About Hawser" onClose={() => close()} footer={<Button primary onClick={() => close()}>Close</Button>}>
      <div style={{ userSelect: "text", lineHeight: 1.7 }}>
        <div style={{ fontSize: 18, fontWeight: 650 }}>Hawser {info.version}</div>
        <div className="dim">Open-source terminal emulator and secure remote access client.</div>
        <div style={{ marginTop: 10 }}>Platform: {info.os}/{info.arch}</div>
        <div>Configuration: <span className="mono">{info.configDir}</span></div>
        <div className="dim" style={{ marginTop: 10 }}>Released under the MIT License.</div>
      </div>
    </Dialog>
  ));
}
