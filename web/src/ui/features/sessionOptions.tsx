// Session options dialog (also edits the Default Session).
import { useCallback, useEffect, useMemo, useState } from "react";
import { rpc } from "../../rpc";
import { ENCODINGS } from "../../term/charsets";
import { EMULATIONS } from "../../term/emulation";
import { COLOR_SCHEMES, ColorScheme } from "../../term/palette";
import { connect, controllers, fail, getApp, patchTab, reloadTree, showDialog, toast, TreeNode, useApp } from "../../state/store";
import { features } from "../features";
import { BAUDS, PROTOCOLS } from "../dialogs";
import { Button, Check, Dialog, Field, Num, Select, Text } from "../kit";
import {
  BCheck, BNum, BPick, BSelect, BText, Bind, clone, ColorInput, deepDiff, deepMerge, getPath, KVList, moveItem, MoveButtons, Pick, Section, setPath, StringList, useComboCapture,
} from "./optionsCommon";
import "./options.css";
import "./optionsGlobal";

interface Arg { path?: string; isNew?: boolean; folder?: string; connId?: number; adhoc?: boolean }
type Secrets = Record<string, string | null>;

const PAGES: { id: string; title: string; show?: (p: string) => boolean; group?: string }[] = [
  { id: "connection", title: "Connection", group: "Session" },
  { id: "logon", title: "Logon Actions" },
  { id: "ssh", title: "SSH2", show: (p) => p === "ssh2", group: "Protocol" },
  { id: "sshadv", title: "SSH2 Advanced", show: (p) => p === "ssh2" },
  { id: "forwards", title: "Port Forwarding", show: (p) => p === "ssh2" },
  { id: "x11", title: "X11", show: (p) => p === "ssh2" },
  { id: "proxy", title: "Proxy" },
  { id: "telnet", title: "Telnet", show: (p) => p === "telnet" || p === "telnet-tls" },
  { id: "rlogin", title: "Rlogin", show: (p) => p === "rlogin" },
  { id: "raw", title: "Raw", show: (p) => p === "raw" },
  { id: "serial", title: "Serial", show: (p) => p === "serial" },
  { id: "local", title: "Local Shell", show: (p) => p === "local" },
  { id: "emulation", title: "Emulation", group: "Terminal" },
  { id: "keyboard", title: "Keyboard & Mouse" },
  { id: "mapped", title: "Mapped Keys" },
  { id: "paste", title: "Paste & Send" },
  { id: "appearance", title: "Appearance" },
  { id: "antiidle", title: "Anti-idle" },
  { id: "log", title: "Log File", group: "Session behaviour" },
  { id: "reconnect", title: "Reconnect" },
  { id: "transfer", title: "File Transfer" },
  { id: "security", title: "Security" },
];

const DEFAULT_PORTS: Record<string, number> = { ssh2: 22, telnet: 23, "telnet-tls": 992, rlogin: 513 };
const AUTHS = [["publickey", "Public key"], ["keyboard-interactive", "Keyboard-interactive"], ["password", "Password"], ["agent", "SSH agent"]] as const;
const clean = (o: any) => {
  const c = clone(o);
  if (c?.logon?.actions) c.logon.actions = c.logon.actions.map((a: any) => ({ expect: a.expect ?? "", send: a.hide ? "" : (a.send ?? ""), hide: !!a.hide }));
  return c;
};

features.sessionOptions = (arg: Arg = {}) => {
  showDialog<void>((close) => <SessionOptions arg={arg} close={() => close()} />);
};

function SessionOptions({ arg, close }: { arg: Arg; close: () => void }) {
  const isDefault = arg.path === "" && !arg.isNew && !arg.adhoc;
  const isNew = !!arg.isNew || !!arg.adhoc;
  const [eff, setEff] = useState<any>(null);
  const [defaults, setDefaults] = useState<any>(null);
  const [have, setHave] = useState<Record<string, boolean>>({});
  const [secrets, setSecrets] = useState<Secrets>({});
  const [name, setName] = useState("");
  const [page, setPage] = useState("connection");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        if (isNew) {
          const d = await rpc.call<any>("sessions.get", { path: "" });
          const base = deepMerge(d.defaults, d.overrides);
          setDefaults(base);
          const t = arg.adhoc ? Object.values(getApp().tabs).find((x) => x.connId === arg.connId) : undefined;
          setEff(t?.info.options ? deepMerge(base, t.info.options) : clone(base));
          if (t) setName(t.info.title?.replace(/[\\/]/g, "_") ?? "");
        } else {
          const d = await rpc.call<any>("sessions.get", { path: arg.path ?? "" });
          setDefaults(d.defaults);
          const e = deepMerge(d.defaults, d.overrides);
          if (e.logon?.actions) e.logon.actions = e.logon.actions.map((a: any, i: number) => ({ ...a, _i: i }));
          setEff(e);
          setHave(d.secrets ?? {});
          setName((arg.path ?? "").split("/").pop() ?? "");
        }
      } catch (e) { fail(e); close(); }
    })();
  }, []);

  const bind: Bind = useMemo(() => ({
    get: (p) => getPath(eff, p),
    set: (p, v) => setEff((o: any) => setPath(o, p, v)),
  }), [eff]);

  const protocol: string = eff?.protocol ?? "ssh2";
  const pages = PAGES.filter((p) => (!p.show || p.show(protocol)));

  useEffect(() => { if (!pages.some((p) => p.id === page)) setPage("connection"); }, [protocol]);

  const setSecret = (k: string, v: string | null | undefined) => setSecrets((s) => { const n = { ...s }; if (v === undefined) delete n[k]; else n[k] = v; return n; });

  async function save(andConnect: boolean) {
    if (busy || !eff) return;
    let path = arg.path ?? "";
    const nm = name.trim();
    if (!isDefault) {
      if (!nm) return toast("A session name is required.", "error"), setPage("connection");
      if (/[\\/]/.test(nm)) return toast("The name cannot contain slashes.", "error"), setPage("connection");
    }
    setBusy(true);
    try {
      const sec: Secrets = { ...secrets };
      const e = clean(eff);
      // logon secrets: keep each saved value bound to its original index
      const rows: any[] = eff.logon?.actions ?? [];
      const maxN = Math.max(rows.length, ...Object.keys(have).filter((k) => k.startsWith("logon:")).map((k) => +k.slice(6) + 1), 0);
      for (let k = 0; k < maxN; k++) {
        const r = rows[k], key = "logon:" + k;
        if (r?.hide) {
          if (r._secret !== undefined) sec[key] = r._secret || null;
          else if (r._i !== k && have[key]) sec[key] = null;
        } else if (have[key]) sec[key] = null;
      }
      if (!isNew || !isDefault) {
        if (sec.password !== undefined) e.savePassword = !!sec.password;
      }
      if (isNew) {
        path = (arg.folder ? arg.folder + "/" : "") + nm;
      } else if (!isDefault) {
        const parent = path.includes("/") ? path.slice(0, path.lastIndexOf("/") + 1) : "";
        const to = parent + nm;
        if (to !== path) {
          await rpc.call("sessions.rename", { from: path, to, folder: false });
          path = to;
        }
      }
      const overrides = deepDiff(e, defaults);
      if (isDefault) await rpc.call("sessions.save", { path: "", overrides });
      else {
        const body: any = { path, overrides, secrets: sec };
        if (isNew) body.mustBeNew = true;
        await rpc.call("sessions.save", body);
      }
      await reloadTree().catch(() => {});
      if (!isDefault) {
        for (const t of Object.values(getApp().tabs)) {
          if (t.kind === "terminal" && t.info.session === path) {
            controllers.get(t.id)?.applyOptions(e);
            patchTab(t.id, { info: { ...t.info, options: e } });
          }
        }
      } else {
        for (const c of controllers.values()) c.applyOptions();
      }
      close();
      if (andConnect && isNew) connect({ session: path });
    } catch (err) {
      fail(err);
      setBusy(false);
    }
  }

  const title = isDefault ? "Default Session Options" : isNew ? "New Session" : `Session Options - ${name || arg.path}`;
  return (
    <Dialog title={title} flush width={860} height={600} onClose={close} onSubmit={() => save(false)}
      footer={<>
        <span className="hint-block spacer" style={{ margin: 0, flex: 1 }}>{isDefault ? "Applies to every session that does not override a value." : "Connection settings take effect on the next reconnect."}</span>
        <Button onClick={close}>Cancel</Button>
        {isNew && <Button disabled={busy} onClick={() => save(true)}>Save &amp; Connect</Button>}
        <Button primary disabled={busy || !eff} onClick={() => save(false)}>{isNew ? "Create" : "OK"}</Button>
      </>}>
      <div className="two-pane opt-dialog">
        <nav aria-label="Categories">
          {pages.map((p) => (
            <div key={p.id}>
              {p.group && <div className="opt-nav-title">{p.group}</div>}
              <div className={"nav-item" + (page === p.id ? " sel" : "")} role="button" tabIndex={0} aria-current={page === p.id}
                onClick={() => setPage(p.id)} onKeyDown={(e) => (e.key === " " || e.key === "Enter") && (e.preventDefault(), e.stopPropagation(), setPage(p.id))}>{p.title}</div>
            </div>
          ))}
        </nav>
        <div className="page" role="region" aria-label={PAGES.find((p) => p.id === page)?.title}>
          {!eff ? <div className="dim">Loading…</div> : (
            <>
              <h3>{PAGES.find((p) => p.id === page)?.title}</h3>
              <PageBody id={page} b={bind} eff={eff} setEff={setEff} isDefault={isDefault} name={name} setName={setName}
                have={have} secrets={secrets} setSecret={setSecret} selfPath={arg.path ?? ""} />
            </>
          )}
        </div>
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------- pages

function SecretInput(p: { label: string; k: string; have: Record<string, boolean>; secrets: Secrets; setSecret: (k: string, v: string | null | undefined) => void; hint?: string }) {
  const v = p.secrets[p.k];
  const saved = !!p.have[p.k] && v === undefined;
  const cleared = v === null;
  return (
    <Field label={p.label} hint={p.hint ?? (cleared ? "Will be removed when you save." : undefined)}>
      <Text type="password" value={v ?? ""} onChange={(x) => p.setSecret(p.k, x)} placeholder={saved ? "(saved - type to replace)" : cleared ? "(removed)" : ""} />
      {(saved || (v && v.length > 0)) && <Button small onClick={() => p.setSecret(p.k, p.have[p.k] ? null : undefined)}>Clear</Button>}
    </Field>
  );
}

function CheckList(p: { all: string[]; insecure: string[]; value: string[]; onChange: (v: string[]) => void; label: string }) {
  const sel = new Set(p.value ?? []);
  const ins = new Set(p.insecure ?? []);
  const toggle = (a: string) => {
    const n = new Set(sel);
    n.has(a) ? n.delete(a) : n.add(a);
    p.onChange([...p.value.filter((x) => n.has(x)), ...p.all.filter((x) => n.has(x) && !p.value.includes(x))]);
  };
  const extra = (p.value ?? []).filter((x) => !p.all.includes(x));
  return (
    <div className="opt-box" role="group" aria-label={p.label}>
      {[...p.all, ...extra].map((a) => (
        <label key={a}><input type="checkbox" checked={sel.has(a)} onChange={() => toggle(a)} /><span className={"mono" + (ins.has(a) ? " bad" : "")}>{a}</span>{ins.has(a) && <span className="bad" style={{ fontSize: 11 }}>insecure</span>}</label>
      ))}
    </div>
  );
}

function Swatch({ s }: { s: ColorScheme }) {
  return <span className="opt-swatch" aria-hidden>{[s.background, s.foreground, ...[1, 2, 3, 4, 5, 6].map((i) => s.ansi[i])].map((c, i) => <i key={i} style={{ background: c }} />)}</span>;
}

function forwardDialog(initial: any): Promise<any | undefined> {
  function Body({ close }: { close: (v?: any) => void }) {
    const [f, setF] = useState<any>(initial);
    const u = (k: string, v: any) => setF((o: any) => ({ ...o, [k]: v }));
    const ok = () => close(f);
    return (
      <Dialog title="Port Forward" width={460} onClose={() => close()} onSubmit={ok} footer={<><Button onClick={() => close()}>Cancel</Button><Button primary onClick={ok}>OK</Button></>}>
        <Field label="Name"><Text value={f.name} onChange={(v) => u("name", v)} autoFocus /></Field>
        <Field label="Type"><Select value={f.type} onChange={(v) => u("type", v)} options={[["local", "Local (-L)"], ["remote", "Remote (-R)"], ["dynamic", "Dynamic SOCKS (-D)"]]} /></Field>
        <Field label="Bind address"><Text value={f.bindAddr} onChange={(v) => u("bindAddr", v)} placeholder="127.0.0.1" /></Field>
        <Field label="Bind port"><Num value={f.bindPort} onChange={(v) => u("bindPort", v)} min={0} max={65535} /></Field>
        {f.type !== "dynamic" && <>
          <Field label="Destination host"><Text value={f.destHost} onChange={(v) => u("destHost", v)} /></Field>
          <Field label="Destination port"><Num value={f.destPort} onChange={(v) => u("destPort", v)} min={0} max={65535} /></Field>
        </>}
        <Check checked={f.enabled} onChange={(v) => u("enabled", v)} label="Enabled" />
      </Dialog>
    );
  }
  return showDialog<any>((close) => <Body close={close} />);
}

function treeSessions(n: TreeNode | null, out: TreeNode[] = []): TreeNode[] {
  if (!n) return out;
  for (const c of n.children ?? []) c.folder ? treeSessions(c, out) : out.push(c);
  return out;
}

function PageBody(p: {
  id: string; b: Bind; eff: any; setEff: (f: (o: any) => any) => void; isDefault: boolean; name: string; setName: (s: string) => void;
  have: Record<string, boolean>; secrets: Secrets; setSecret: (k: string, v: string | null | undefined) => void; selfPath: string;
}) {
  const { b } = p;
  const app = useApp();
  const [ports, setPorts] = useState<string[]>([]);
  const [capIdx, setCapIdx] = useState(-1);
  useEffect(() => { if (p.id === "serial") rpc.call<string[]>("serial.ports").then(setPorts).catch(() => {}); }, [p.id]);
  const onCombo = useCallback((c: string | null) => {
    setCapIdx((i) => {
      if (c && i >= 0) b.set("terminal.keymap", (b.get("terminal.keymap") ?? []).map((k: any, j: number) => (j === i ? { ...k, key: c } : k)));
      return -1;
    });
  }, [b]);
  useComboCapture(capIdx >= 0, onCombo);

  switch (p.id) {
    case "connection": {
      const sessions = treeSessions(app.tree).filter((s) => s.path !== p.selfPath && (!s.summary || s.summary.protocol === "ssh2"));
      const fw = b.get("firewall") || "none";
      return (<>
        {!p.isDefault && <Field label="Name"><Text value={p.name} onChange={p.setName} autoFocus placeholder="Session name" /></Field>}
        <Field label="Protocol"><Select value={b.get("protocol")} options={PROTOCOLS} onChange={(v) => {
          const old = b.get("protocol");
          p.setEff((o: any) => {
            let n = setPath(o, "protocol", v);
            if (v in DEFAULT_PORTS && (!n.port || n.port === DEFAULT_PORTS[old] || !(old in DEFAULT_PORTS))) n.port = DEFAULT_PORTS[v];
            else if (!(v in DEFAULT_PORTS) && n.port === DEFAULT_PORTS[old]) n.port = 0;
            return n;
          });
        }} /></Field>
        {!["serial", "local"].includes(b.get("protocol")) && <>
          {!p.isDefault && BText(b, "Host", "host", { placeholder: "hostname or address" })}
          <Field label="Port" hint={DEFAULT_PORTS[b.get("protocol")] ? `Default for ${b.get("protocol")}: ${DEFAULT_PORTS[b.get("protocol")]}` : undefined}>
            <Num value={b.get("port")} onChange={(v) => b.set("port", v)} min={0} max={65535} />
          </Field>
        </>}
        {["ssh2", "telnet", "telnet-tls", "rlogin"].includes(b.get("protocol")) && BText(b, "Username", "username")}
        {b.get("protocol") === "ssh2" && <CredentialSelect value={b.get("credential") ?? ""} onChange={(v) => b.set("credential", v)} />}
        {!p.isDefault && ["ssh2", "telnet", "telnet-tls", "rlogin"].includes(b.get("protocol")) &&
          <SecretInput label="Password" k="password" have={p.have} secrets={p.secrets} setSecret={p.setSecret} hint="Stored encrypted in the credential vault." />}
        {!p.isDefault && BText(b, "Description", "description")}
        {!["serial", "local"].includes(b.get("protocol")) &&
          <Field label="Firewall" hint="Reach the host directly, through the proxy configured on the Proxy page, or via another SSH2 session (jump host).">
            <Select value={fw.startsWith("session:") ? fw : fw} onChange={(v) => b.set("firewall", v)} options={[
              ["none", "None (direct)"], ["proxy", "Proxy"],
              ...sessions.map((s) => ["session:" + s.path, "Session: " + s.path] as const),
              ...(fw.startsWith("session:") && !sessions.some((s) => "session:" + s.path === fw) ? [[fw, fw] as const] : []),
            ]} />
          </Field>}
      </>);
    }
    case "logon": {
      const rows: any[] = b.get("logon.actions") ?? [];
      const setRows = (r: any[]) => b.set("logon.actions", r);
      const upd = (i: number, k: string, v: any) => setRows(rows.map((r, j) => (j === i ? { ...r, [k]: v } : r)));
      return (<>
        {BCheck(b, "Enable logon actions", "logon.enabled", "Wait for text from the host and answer it automatically after connecting.")}
        {BNum(b, "Timeout (seconds)", "logon.timeout", { hint: "How long to wait for each expected string.", min: 1 })}
        <Section>Actions</Section>
        <div className="opt-list">
          {rows.map((r, i) => (
            <div className="row" key={i}>
              <Text value={r.expect ?? ""} onChange={(v) => upd(i, "expect", v)} placeholder="Expect (e.g. login:)" mono />
              <Text type={r.hide ? "password" : "text"} value={r._secret ?? (r.hide ? "" : r.send ?? "")}
                onChange={(v) => r.hide ? upd(i, "_secret", v) : upd(i, "send", v)} mono
                placeholder={r.hide ? (r._i === i && p.have["logon:" + i] && r._secret === undefined ? "(saved)" : "Send (hidden)") : "Send"} />
              <Check checked={!!r.hide} onChange={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, hide: v, send: v ? "" : x.send, _secret: v ? x._secret : undefined } : x)))} label="Hide" />
              <MoveButtons i={i} n={rows.length} move={(a, d) => setRows(moveItem(rows, a, d))} />
              <Button small onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove</Button>
            </div>
          ))}
          <div><Button small onClick={() => setRows([...rows, { expect: "", send: "", hide: false }])}>Add action</Button></div>
        </div>
        <div className="hint-block">Hidden values are kept in the credential vault and are never shown or logged. Reordering or removing actions asks you to re-enter hidden values whose position changed. Use \r for Enter in Send.</div>
        <Section>After logon</Section>
        {BPick(b, "Logon script", "logon.script", "open", "Script to run once connected.")}
        {BText(b, "Script arguments", "logon.scriptArgs", { mono: true })}
      </>);
    }
    case "ssh": {
      const auth: string[] = b.get("ssh.auth") ?? [];
      const order = [...auth, ...AUTHS.map((a) => a[0]).filter((a) => !auth.includes(a))];
      return (<>
        <Section>Authentication</Section>
        <div className="opt-list" role="group" aria-label="Authentication methods">
          {order.map((m, i) => (
            <div className="row" key={m}>
              <Check checked={auth.includes(m)} onChange={(v) => b.set("ssh.auth", v ? [...auth, m] : auth.filter((x) => x !== m))}
                label={AUTHS.find((a) => a[0] === m)?.[1] ?? m} />
              <span className="grow" />
              {auth.includes(m) && <MoveButtons i={auth.indexOf(m)} n={auth.length} move={(a, d) => b.set("ssh.auth", moveItem(auth, a, d))} />}
            </div>
          ))}
        </div>
        <div className="hint-block">Enabled methods are tried in the order shown.</div>
        <Field label="Identity files"><span className="dim">Private keys to offer</span></Field>
        <StringList label="identity file" file value={b.get("ssh.identityFiles")} onChange={(v) => b.set("ssh.identityFiles", v)} />
        {BCheck(b, "Use SSH agent", "ssh.useAgent")}
        {BCheck(b, "Forward agent", "ssh.agentForwarding")}
        <Section>Host keys</Section>
        {BSelect(b, "Unknown host keys", "ssh.hostKeyPolicy", [["ask", "Ask me"], ["accept-new", "Accept new, reject changed"], ["strict", "Strict (must be known)"], ["off", "Do not verify (unsafe)"]])}
        {BCheck(b, "Also use OpenSSH known_hosts", "ssh.useOpenSSHKnownHosts")}
        <Section>Session</Section>
        {BNum(b, "Keep-alive (seconds)", "ssh.keepAliveInterval", { hint: "0 = off", min: 0 })}
        {BNum(b, "Connect timeout (s)", "ssh.connectTimeout", { min: 1 })}
        {BText(b, "Remote command", "ssh.remoteCommand", { mono: true, hint: "Run instead of the login shell." })}
        {BText(b, "Subsystem", "ssh.subsystem", { mono: true })}
        {BText(b, "SFTP subsystem", "ssh.sftpSubsystem", { mono: true })}
        {BCheck(b, "Request a pseudo-terminal", "ssh.requestPty")}
        {BCheck(b, "Show server banner", "ssh.showBanner")}
        <Section>Environment variables</Section>
        <KVList label="variable" value={b.get("ssh.env")} onChange={(v) => b.set("ssh.env", v)} />
      </>);
    }
    case "sshadv": {
      const al = app.info?.sshAlgorithms ?? {};
      const cl = (label: string, path: string, all: string, ins: string) => (
        <>
          <Section>{label}</Section>
          <CheckList label={label} all={al[all] ?? []} insecure={al[ins] ?? []} value={b.get(path) ?? []} onChange={(v) => b.set(path, v)} />
        </>
      );
      return (<>
        <div className="hint-block">Leave everything unchecked to use the library defaults. Checked algorithms are offered in the order of the list.</div>
        {cl("Key exchange", "ssh.kexAlgorithms", "kex", "insecureKex")}
        {cl("Ciphers", "ssh.ciphers", "ciphers", "insecureCiphers")}
        {cl("MACs", "ssh.macs", "macs", "insecureMacs")}
        {cl("Host key algorithms", "ssh.hostKeyAlgorithms", "hostKeys", "insecureHostKeys")}
      </>);
    }
    case "forwards": {
      const rows: any[] = b.get("ssh.forwards") ?? [];
      const setRows = (r: any[]) => b.set("ssh.forwards", r);
      const blank = { name: "", type: "local", bindAddr: "127.0.0.1", bindPort: 0, destHost: "", destPort: 0, enabled: true };
      const edit = async (i: number) => { const r = await forwardDialog(i < 0 ? blank : rows[i]); if (r) setRows(i < 0 ? [...rows, r] : rows.map((x, j) => (j === i ? r : x))); };
      return (<>
        <div className="opt-grid-scroll"><table className="grid">
          <thead><tr><th>On</th><th>Name</th><th>Type</th><th>Bind</th><th>Destination</th><th></th></tr></thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                <td><input type="checkbox" aria-label={`Enable ${r.name || "forward " + (i + 1)}`} checked={!!r.enabled} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, enabled: e.target.checked } : x)))} /></td>
                <td>{r.name}</td><td>{r.type}</td><td>{r.bindAddr}:{r.bindPort}</td><td>{r.type === "dynamic" ? "SOCKS" : `${r.destHost}:${r.destPort}`}</td>
                <td><div className="row"><Button small onClick={() => edit(i)}>Edit</Button><Button small onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove</Button></div></td>
              </tr>
            ))}
          </tbody>
        </table></div>
        {!rows.length && <div className="hint-block">No port forwards.</div>}
        <div style={{ marginTop: 8 }}><Button small onClick={() => edit(-1)}>Add forward…</Button></div>
      </>);
    }
    case "x11":
      return (<>
        {BCheck(b, "Forward X11 connections", "ssh.x11Forwarding")}
        {BText(b, "X display", "ssh.x11Display", { placeholder: "localhost:0", mono: true, hint: "Where the local X server listens; empty uses $DISPLAY." })}
      </>);
    case "proxy":
      return (<>
        {BSelect(b, "Type", "proxy.type", [["none", "None"], ["socks4", "SOCKS4"], ["socks4a", "SOCKS4a"], ["socks5", "SOCKS5"], ["http", "HTTP CONNECT"]],
          b.get("firewall") === "proxy" ? undefined : "Set Firewall to \"Proxy\" on the Connection page to use it.")}
        {BText(b, "Host", "proxy.host")}
        {BNum(b, "Port", "proxy.port", { min: 0, max: 65535 })}
        {BText(b, "Username", "proxy.username")}
        {!p.isDefault && <SecretInput label="Password" k="proxy" have={p.have} secrets={p.secrets} setSecret={p.setSecret} />}
      </>);
    case "telnet":
      return (<>
        {BCheck(b, "Negotiate window size (NAWS)", "telnet.naws")}
        {BText(b, "Terminal speed", "telnet.terminalSpeed", { mono: true })}
        {BCheck(b, "Force character mode", "telnet.forceCharMode")}
        {BCheck(b, "Verify TLS certificate", "telnet.tlsVerify", "Telnet/TLS only.")}
        {BSelect(b, "Break sends", "telnet.breakSends", [["telnet", "Telnet BREAK (IAC BRK)"], ["ctrl-c", "Ctrl+C"]])}
        <Section>Environment variables</Section>
        <KVList label="variable" value={b.get("telnet.env")} onChange={(v) => b.set("telnet.env", v)} />
      </>);
    case "rlogin":
      return (<>{BText(b, "Local user", "rlogin.localUser")}{BText(b, "Terminal speed", "rlogin.terminalSpeed", { mono: true })}</>);
    case "raw":
      return (<>{BCheck(b, "Use TLS", "raw.tls")}{BCheck(b, "Verify TLS certificate", "raw.tlsVerify")}
        <div className="hint-block">Local echo and line-ending translation are on the Emulation and Paste &amp; Send pages.</div></>);
    case "serial":
      return (<>
        <Field label="Port" hint="Choose a detected port or type a device path.">
          <Text value={b.get("serial.port") ?? ""} onChange={(v) => b.set("serial.port", v)} mono placeholder="/dev/ttyUSB0" />
          {ports.length > 0 && <Select value="" width={160} onChange={(v) => v && b.set("serial.port", v)} options={[["", "Detected…"], ...ports.map((x) => [x, x] as const)]} />}
        </Field>
        <Field label="Baud rate"><Select value={String(b.get("serial.baud"))} onChange={(v) => b.set("serial.baud", Number(v))}
          options={[...new Set([...BAUDS, b.get("serial.baud")])].map((x) => [String(x), String(x)] as const)} /></Field>
        <Field label="Data bits"><Select value={String(b.get("serial.dataBits"))} onChange={(v) => b.set("serial.dataBits", Number(v))} options={["5", "6", "7", "8"].map((x) => [x, x] as const)} /></Field>
        {BSelect(b, "Parity", "serial.parity", [["none", "None"], ["odd", "Odd"], ["even", "Even"], ["mark", "Mark"], ["space", "Space"]])}
        {BSelect(b, "Stop bits", "serial.stopBits", [["1", "1"], ["1.5", "1.5"], ["2", "2"]])}
        {BSelect(b, "Flow control", "serial.flow", [["none", "None"], ["rtscts", "RTS/CTS"], ["xonxoff", "XON/XOFF"], ["dtrdsr", "DTR/DSR"]])}
        {BCheck(b, "Assert DTR", "serial.dtr")}
        {BCheck(b, "Assert RTS", "serial.rts")}
      </>);
    case "local":
      return (<>
        {BPick(b, "Shell", "local.shell", "open", "Empty uses $SHELL.")}
        <Field label="Arguments"><span className="dim">One per line below</span></Field>
        <StringList label="argument" value={b.get("local.args")} onChange={(v) => b.set("local.args", v)} />
        {BPick(b, "Start directory", "local.cwd", "folder")}
        {BCheck(b, "Login shell", "local.login")}
        <Section>Environment variables</Section>
        <KVList label="variable" value={b.get("local.env")} onChange={(v) => b.set("local.env", v)} />
      </>);
    case "emulation": {
      const em = EMULATIONS.find((e) => e.id === b.get("terminal.emulation"));
      return (<>
        {BSelect(b, "Emulation", "terminal.emulation", EMULATIONS.map((e) => [e.id, e.label] as const))}
        {BText(b, "Terminal type", "terminal.termType", { placeholder: em?.termType, mono: true, hint: "Overrides TERM sent to the host; empty uses the emulation's default." })}
        {BSelect(b, "Encoding", "terminal.encoding", ENCODINGS.map((e) => [e.id, e.label] as const))}
        {BNum(b, "Scrollback (lines)", "terminal.scrollback", { min: 0, max: 1000000, width: 110 })}
        {BSelect(b, "Size", "terminal.sizeMode", [["fit", "Fit window"], ["fixed", "Fixed size"]])}
        {b.get("terminal.sizeMode") === "fixed" && <Field label="Columns x rows"><Num value={b.get("terminal.cols")} min={20} max={500} onChange={(v) => b.set("terminal.cols", v)} width={80} /><span>x</span><Num value={b.get("terminal.rows")} min={5} max={300} onChange={(v) => b.set("terminal.rows", v)} width={80} /></Field>}
        {BText(b, "Answerback", "terminal.answerback", { mono: true, hint: "Text sent in reply to ENQ." })}
        {BSelect(b, "Cursor", "terminal.cursorStyle", [["block", "Block"], ["underline", "Underline"], ["bar", "Bar"]])}
        {BCheck(b, "Blinking cursor", "terminal.cursorBlink")}
        {BSelect(b, "Bell", "terminal.bell", [["visual", "Visual"], ["audible", "Audible"], ["both", "Both"], ["none", "None"]])}
        {BCheck(b, "New line mode (LF sends CR+LF)", "terminal.newLineMode")}
        {BCheck(b, "Auto-wrap", "terminal.autoWrap")}
        {BCheck(b, "ANSI colour", "terminal.ansiColor")}
        {BCheck(b, "Allow 80/132 column switching", "terminal.allow132")}
        {BCheck(b, "Interpret 8-bit C1 controls", "terminal.c1Controls")}
        {BCheck(b, "Local echo", "terminal.localEcho")}
        {BCheck(b, "Clear screen scrolls content into scrollback", "terminal.scrollToClear")}
      </>);
    }
    case "keyboard":
      return (<>
        {BSelect(b, "Backspace sends", "terminal.backspace", [["del", "Delete (^?)"], ["bs", "Backspace (^H)"]])}
        {BSelect(b, "Delete key sends", "terminal.deleteKey", [["vt", "Escape sequence (VT)"], ["del", "Delete (^?)"]])}
        {BCheck(b, "Alt acts as Meta (sends ESC prefix)", "terminal.altIsMeta")}
        <Section>Mouse</Section>
        {BCheck(b, "Copy on select", "terminal.copyOnSelect")}
        {BSelect(b, "Right click", "terminal.rightClick", [["menu", "Context menu"], ["paste", "Paste"]])}
        {BSelect(b, "Middle click", "terminal.middleClick", [["paste", "Paste"], ["none", "Nothing"]])}
        {BText(b, "Word delimiters", "terminal.wordDelimiters", { mono: true, hint: "Characters that end a word on double-click." })}
        {BCheck(b, "Let applications receive mouse reports", "terminal.allowMouseReports")}
        <Section>Scrolling</Section>
        {BCheck(b, "Scroll to bottom on key press", "terminal.scrollOnKey")}
        {BCheck(b, "Scroll to bottom on output", "terminal.scrollOnOutput")}
      </>);
    case "mapped": {
      const rows: any[] = b.get("terminal.keymap") ?? [];
      const setRows = (r: any[]) => b.set("terminal.keymap", r);
      return (<>
        <div className="hint-block">Map a key combination to text sent to the host. Escapes: \r \n \t \e \xHH.</div>
        <div className="opt-list">
          {rows.map((r, i) => (
            <div className="row" key={i}>
              <Button small title="Click, then press the key combination" onClick={() => setCapIdx(capIdx === i ? -1 : i)}>
                {capIdx === i ? "Press keys…" : r.key || "Set key…"}
              </Button>
              <Select value={r.action ?? "send"} width={110} onChange={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, action: v } : x)))} options={[["send", "Send text"], ["disabled", "Disabled"]]} />
              <Text value={r.value ?? ""} disabled={r.action === "disabled"} mono placeholder="Text, e.g. ls -l\r" onChange={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, value: v } : x)))} />
              <Button small onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove</Button>
            </div>
          ))}
          <div><Button small onClick={() => { setRows([...rows, { key: "", action: "send", value: "" }]); setCapIdx(rows.length); }}>Add mapped key</Button></div>
        </div>
      </>);
    }
    case "paste":
      return (<>
        {BCheck(b, "Confirm multi-line paste", "terminal.confirmMultilinePaste")}
        {BCheck(b, "Trim trailing whitespace when pasting", "terminal.trimPaste")}
        {BSelect(b, "Enter / newline sends", "terminal.newlineOnSend", [["cr", "CR"], ["lf", "LF"], ["crlf", "CR+LF"]])}
        {BNum(b, "Line send delay (ms)", "terminal.lineSendDelay", { min: 0, hint: "Pause after each line of pasted or scripted text." })}
        {BNum(b, "Character delay (ms)", "terminal.charSendDelay", { min: 0, hint: "Pause between characters, for slow devices." })}
      </>);
    case "appearance": {
      const g = app.global;
      const schemes: ColorScheme[] = [...COLOR_SCHEMES, ...(g.customSchemes ?? [])];
      const cur = b.get("appearance.colorScheme") ?? "";
      const tab = b.get("appearance.tabColor") ?? "";
      return (<>
        <Section>Colours</Section>
        <div className="opt-box" role="radiogroup" aria-label="Colour scheme" style={{ maxHeight: 170, marginBottom: 10 }}>
          <label><input type="radio" name="scheme" checked={cur === ""} onChange={() => b.set("appearance.colorScheme", "")} /><span>Use global default</span></label>
          {schemes.map((s) => <label key={s.id}><input type="radio" name="scheme" checked={cur === s.id} onChange={() => b.set("appearance.colorScheme", s.id)} /><Swatch s={s} /><span>{s.name}</span></label>)}
        </div>
        <Field label="Tab colour" hint="Accent shown on the tab and in the session list.">
          <ColorInput label="Tab colour" value={tab} onChange={(v) => b.set("appearance.tabColor", v)} />
          <Text value={tab} onChange={(v) => b.set("appearance.tabColor", v)} placeholder="none" width={110} mono />
          <Button small disabled={!tab} onClick={() => b.set("appearance.tabColor", "")}>Clear</Button>
        </Field>
        <Section>Font</Section>
        {BText(b, "Font family", "appearance.fontFamily", { placeholder: "global default", hint: "Comma separated CSS font names." })}
        {BNum(b, "Font size", "appearance.fontSize", { min: 0, max: 72, step: 0.5, hint: "0 = global default" })}
        {BNum(b, "Line height", "appearance.lineHeight", { min: 0.8, max: 2.5, step: 0.05 })}
        {BCheck(b, "Bold text uses bright colours", "appearance.boldAsBright")}
        {BCheck(b, "Use a bold font for bold text", "appearance.boldFont")}
        {BCheck(b, "Allow blinking text", "appearance.blink")}
        {BCheck(b, "Font ligatures", "appearance.ligatures")}
        <Section>Highlighting</Section>
        {BSelect(b, "Keyword set", "terminal.keywordSet", [["", "None"], ...Object.keys(g.keywordSets ?? {}).map((k) => [k, k] as const)])}
      </>);
    }
    case "antiidle":
      return (<>
        {BCheck(b, "Send something periodically to avoid idle timeouts", "terminal.antiIdle.enabled")}
        {BText(b, "Text to send", "terminal.antiIdle.text", { mono: true, hint: "Empty sends a protocol no-op. Escapes like \\r are allowed." })}
        {BNum(b, "Interval (seconds)", "terminal.antiIdle.interval", { min: 1 })}
      </>);
    case "log":
      return (<>
        {BText(b, "Log file", "log.file", { mono: true, hint: "%H host  %S session  %P port  %U user  %Y year  %M month  %D day  %h hour  %m minute  %s second  %t time stamp  %% percent" })}
        {BCheck(b, "Start logging on connect", "log.startOnConnect")}
        {BCheck(b, "Ask for the file name each time", "log.promptForFile")}
        {BCheck(b, "Raw log (include control sequences)", "log.raw")}
        {BCheck(b, "Append to an existing file", "log.append")}
        {BCheck(b, "Start a new file at midnight", "log.newFileAtMidnight")}
        {BCheck(b, "Log only custom text", "log.onlyCustom", "Only the strings below are written.")}
        <Section>Custom strings</Section>
        {BText(b, "On connect", "log.onConnect", { mono: true })}
        {BText(b, "On disconnect", "log.onDisconnect", { mono: true })}
        {BText(b, "On each line", "log.onEachLine", { mono: true })}
      </>);
    case "reconnect":
      return (<>
        {BCheck(b, "Reconnect automatically when disconnected", "connection.autoReconnect")}
        {BNum(b, "Delay (seconds)", "connection.reconnectDelay", { min: 0 })}
        {BCheck(b, "Close the tab when disconnected", "connection.closeOnDisconnect")}
      </>);
    case "transfer":
      return (<>
        {BPick(b, "Download folder", "transfer.downloadDir", "folder")}
        {BPick(b, "Upload folder", "transfer.uploadDir", "folder")}
        {BText(b, "SFTP start directory", "transfer.sftpInitialDir", { mono: true })}
        <Section>ZMODEM</Section>
        {BCheck(b, "Start automatically", "transfer.zmodemAuto")}
        {BCheck(b, "Overwrite existing files", "transfer.zmodemOverwrite")}
        {BCheck(b, "Resume interrupted transfers", "transfer.zmodemResume")}
        <Section>XMODEM</Section>
        {BCheck(b, "Use 1K blocks", "transfer.xmodem1k")}
        {BCheck(b, "Use CRC", "transfer.xmodemCrc")}
      </>);
    case "security":
      return (<>
        {BCheck(b, "Allow the host to change the window title", "terminal.allowTitleChange")}
        {BCheck(b, "Allow the host to write to the clipboard", "terminal.allowClipboardWrite")}
        {BSelect(b, "Printing", "terminal.printer", [["dialog", "Open print dialog"], ["file", "Save to file"], ["ignore", "Ignore"]])}
      </>);
  }
  return null;
}

/** Chooses a shared credential (Tools → Credentials…) instead of the session's own login. */
function CredentialSelect(p: { value: string; onChange: (v: string) => void }) {
  const [names, setNames] = useState<string[]>([]);
  useEffect(() => {
    rpc.call<{ name: string }[]>("credentials.list").then((l) => setNames(l.map((c) => c.name))).catch(() => {});
  }, []);
  const opts: (readonly [string, string])[] = [["", "None (use the username and password above)"], ...names.map((n) => [n, n] as const)];
  if (p.value && !names.includes(p.value)) opts.push([p.value, p.value + " (missing)"]);
  return (
    <Field label="Credential" hint="A shared login from Tools → Credentials… overrides the username and password.">
      <Select value={p.value} onChange={p.onChange} options={opts} />
    </Field>
  );
}
