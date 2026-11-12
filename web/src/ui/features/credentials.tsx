// Shared credentials: named user name / password pairs kept in the vault
// that several sessions can reference.
import { Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { rpc } from "../../rpc";
import { fail, showDialog } from "../../state/store";
import { confirm } from "../dialogs";
import { features } from "../features";
import { Button, Dialog, Field, Text } from "../kit";

interface Cred { name: string; username?: string; hasPassword?: boolean }

function Credentials({ close }: { close: () => void }) {
  const [list, setList] = useState<Cred[]>([]);
  const [sel, setSel] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [dirty, setDirty] = useState(false);
  const load = () => rpc.call<Cred[]>("credentials.list").then(setList).catch(fail);
  useEffect(() => { load(); }, []);
  const pick = (c: Cred | null) => {
    setSel(c ? c.name : "");
    setName(c?.name ?? "");
    setUsername(c?.username ?? "");
    setPassword("");
    setDirty(false);
  };
  const save = async () => {
    if (!name.trim()) return;
    try {
      await rpc.call("credentials.save", { name: name.trim(), rename: sel || undefined, username, password: password ? password : sel ? undefined : "" });
      await load();
      setSel(name.trim());
      setPassword("");
      setDirty(false);
    } catch (e) {
      fail(e);
    }
  };
  const remove = async () => {
    if (!sel || !(await confirm("Delete Credential", `Delete "${sel}"? Sessions that use it will fail to connect until they are changed.`, "Delete", true))) return;
    await rpc.call("credentials.delete", { name: sel }).catch(fail);
    setSel(null);
    load();
  };
  const cur = list.find((c) => c.name === sel);
  return (
    <Dialog title="Credentials" width={640} height={420} flush onClose={close}
      footer={<><span className="dim" style={{ flex: 1 }}>Pick a credential on a session's Connection page to use it.</span><Button onClick={close}>Close</Button></>}>
      <div className="two-pane">
        <nav aria-label="Credentials">
          {list.map((c) => (
            <div key={c.name} className={"nav-item" + (c.name === sel ? " sel" : "")} onClick={() => pick(c)}>{c.name}</div>
          ))}
          {list.length === 0 && <div className="dim" style={{ padding: 8 }}>No credentials yet.</div>}
          <div style={{ padding: "8px 4px" }}><button className="btn small" onClick={() => pick(null)}><Plus size={13} /> New</button></div>
        </nav>
        <div className="page">
          {sel === null ? <div className="dim">Select a credential, or create one. A credential stores a user name and password once, encrypted in the vault, for any number of sessions.</div> : <>
            <Field label="Name"><Text value={name} onChange={(v) => { setName(v); setDirty(true); }} autoFocus={!sel} /></Field>
            <Field label="Username"><Text value={username} onChange={(v) => { setUsername(v); setDirty(true); }} /></Field>
            <Field label="Password" hint={cur?.hasPassword ? "A password is stored. Leave empty to keep it." : "Stored encrypted in the credential vault."}>
              <Text value={password} type="password" onChange={(v) => { setPassword(v); setDirty(true); }} />
            </Field>
            <div className="row" style={{ marginTop: 12 }}>
              <Button primary disabled={!name.trim() || !dirty} onClick={save}>Save</Button>
              {sel && <Button danger onClick={remove}><Trash2 size={13} /> Delete</Button>}
            </div>
          </>}
        </div>
      </div>
    </Dialog>
  );
}

features.credentials = () => void showDialog<void>((close) => <Credentials close={() => close()} />);
