import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { rpc } from "./rpc";
import { activeController, boot, connect, ui, useApp } from "./state/store";
import "./styles.css";
import { registerEnginePrompts } from "./ui/dialogs";
import { feature } from "./ui/features";
import "./ui/plugins";
import { Workbench } from "./ui/Workbench";

function App() {
  const phase = useApp((s) => s.phase);
  const err = useApp((s) => s.bootError);
  const [token, setToken] = useState("");
  useEffect(() => {
    registerEnginePrompts();
    ui.onStartup = (actions) => {
      for (const a of actions) {
        if (a.action === "connect") void connect({ session: a.session, overrides: a.overrides, password: a.password, title: a.title });
        else if (a.action === "sftp") feature("sftpOpen", { overrides: a.overrides, password: a.password });
        else if (a.action === "script") feature("scriptRun", { path: a.path, args: a.args });
      }
    };
    boot();
  }, []);
  if (phase === "ready") return <Workbench />;
  return (
    <div className="boot">
      <div className="card">
        {phase === "boot" && <div>Starting Hawser …</div>}
        {phase === "lost" && <><h3 style={{ marginTop: 0 }}>Connection to the engine lost</h3><p className="dim">The Hawser process is no longer running. Start it again, then reload.</p>
          <button className="btn primary" onClick={() => location.reload()}>Reload</button></>}
        {phase === "token" && <>
          <h3 style={{ marginTop: 0 }}>Access token required</h3>
          <p className="dim">Open the address printed by <span className="mono">hawser</span> when it started, or paste its access token here.</p>
          {err && <p className="error-text">{err}</p>}
          <div className="row">
            <input className="input grow" type="password" value={token} placeholder="token" onChange={(e) => setToken(e.target.value)} aria-label="Access token" />
            <button className="btn primary" onClick={() => { rpc.setToken(token.trim()); location.reload(); }}>Connect</button>
          </div>
        </>}
      </div>
    </div>
  );
}

// test hook: lets end-to-end tests read the active terminal's screen
(window as any).__hawser = {
  screen: () => activeController()?.term.screenText() ?? [],
  ctrl: () => activeController(),
};

createRoot(document.getElementById("root")!).render(<App />);
