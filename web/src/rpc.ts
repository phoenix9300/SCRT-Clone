// WebSocket client: JSON requests/events plus binary terminal frames.

type Pending = { resolve: (v: any) => void; reject: (e: Error) => void };
type EventHandler = (data: any) => void;
type RequestHandler = (data: any, signal: AbortSignal) => Promise<any>;

const FRAME_OUTPUT = 1;
const FRAME_INPUT = 2;

class Rpc {
  private ws: WebSocket | null = null;
  private nextId = 1;
  private pending = new Map<number, Pending>();
  private events = new Map<string, Set<EventHandler>>();
  private requests = new Map<string, RequestHandler>();
  private aborts = new Map<number, AbortController>();
  private dataHandlers = new Map<number, (p: Uint8Array) => void>();
  private queue: (string | Uint8Array)[] = [];
  private orphans = new Map<number, Uint8Array[]>();
  private taps = new Set<(dir: "in" | "out", connId: number, data: Uint8Array) => void>();
  token = "";
  connected = false;
  onStatus: (connected: boolean, reason?: string) => void = () => {};

  /** Reads the access token from the URL (and hides it) or session storage. */
  init(): void {
    const u = new URL(location.href);
    const t = u.searchParams.get("token");
    if (t) {
      sessionStorage.setItem("hawser-token", t);
      u.searchParams.delete("token");
      history.replaceState(null, "", u.pathname + u.search + u.hash);
    }
    this.token = t ?? sessionStorage.getItem("hawser-token") ?? "";
  }

  setToken(t: string): void {
    this.token = t;
    sessionStorage.setItem("hawser-token", t);
  }

  connect(): Promise<void> {
    return new Promise((resolve, reject) => {
      const proto = location.protocol === "https:" ? "wss" : "ws";
      const ws = new WebSocket(`${proto}://${location.host}/ws?token=${encodeURIComponent(this.token)}`);
      ws.binaryType = "arraybuffer";
      let opened = false;
      ws.onopen = () => {
        opened = true;
        this.ws = ws;
        this.connected = true;
        for (const m of this.queue) ws.send(m as any);
        this.queue = [];
        this.onStatus(true);
        resolve();
      };
      ws.onclose = () => {
        this.connected = false;
        this.ws = null;
        for (const p of this.pending.values()) p.reject(new Error("connection to the Hawser engine was lost"));
        this.pending.clear();
        if (!opened) reject(new Error("cannot connect to the Hawser engine (wrong or missing access token?)"));
        else this.onStatus(false, "The connection to the Hawser engine was lost.");
      };
      ws.onmessage = (ev) => this.onMessage(ev.data);
    });
  }

  private onMessage(data: string | ArrayBuffer): void {
    if (typeof data !== "string") {
      const b = new Uint8Array(data);
      if (b.length >= 5 && b[0] === FRAME_OUTPUT) {
        const id = ((b[1] << 24) | (b[2] << 16) | (b[3] << 8) | b[4]) >>> 0;
        if (this.taps.size) for (const t of this.taps) t("in", id, b.subarray(5));
        const h = this.dataHandlers.get(id);
        if (h) h(b.subarray(5));
        else {
          // output for a connection whose tab is still being created
          let q = this.orphans.get(id);
          if (!q) this.orphans.set(id, (q = []));
          if (q.length < 4096) q.push(b.slice(5));
        }
      }
      return;
    }
    const m = JSON.parse(data);
    if (m.event !== undefined) {
      if (m.event === "request.cancel") {
        this.aborts.get(m.data.rid)?.abort();
        return;
      }
      this.events.get(m.event)?.forEach((h) => h(m.data));
    } else if (m.rid !== undefined) {
      const h = this.requests.get(m.request);
      const ac = new AbortController();
      this.aborts.set(m.rid, ac);
      const done = (payload: object) => {
        this.aborts.delete(m.rid);
        this.sendRaw(JSON.stringify({ rid: m.rid, ...payload }));
      };
      if (!h) done({ error: "unsupported request " + m.request });
      else h(m.data, ac.signal).then((result) => done({ result }), (e) => done({ error: String(e?.message ?? e) }));
    } else if (m.id !== undefined) {
      const p = this.pending.get(m.id);
      if (!p) return;
      this.pending.delete(m.id);
      if (m.error !== undefined) p.reject(new Error(m.error));
      else p.resolve(m.result);
    }
  }

  private sendRaw(m: string | Uint8Array): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(m as any);
    else this.queue.push(m);
  }

  call<T = any>(method: string, params?: unknown): Promise<T> {
    const id = this.nextId++;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.sendRaw(JSON.stringify({ id, method, params }));
    });
  }

  /** Fire-and-forget call (no response expected). */
  notify(method: string, params?: unknown): void {
    this.sendRaw(JSON.stringify({ method, params }));
  }

  on(event: string, h: EventHandler): () => void {
    let set = this.events.get(event);
    if (!set) this.events.set(event, (set = new Set()));
    set.add(h);
    return () => set!.delete(h);
  }

  /** Register a handler for requests initiated by the engine (prompts, script dialogs). */
  handle(name: string, h: RequestHandler): void {
    this.requests.set(name, h);
  }

  onData(connId: number, h: ((p: Uint8Array) => void) | null): void {
    if (h) {
      this.dataHandlers.set(connId, h);
      const q = this.orphans.get(connId);
      this.orphans.delete(connId);
      q?.forEach((p) => h(p));
    } else {
      this.dataHandlers.delete(connId);
      this.orphans.delete(connId);
    }
  }

  /** Observe raw terminal traffic in both directions (hex view). */
  tap(fn: (dir: "in" | "out", connId: number, data: Uint8Array) => void): () => void {
    this.taps.add(fn);
    return () => this.taps.delete(fn);
  }

  sendInput(connId: number, data: Uint8Array): void {
    if (this.taps.size) for (const t of this.taps) t("out", connId, data);
    const f = new Uint8Array(5 + data.length);
    f[0] = FRAME_INPUT;
    f[1] = connId >>> 24;
    f[2] = (connId >>> 16) & 0xff;
    f[3] = (connId >>> 8) & 0xff;
    f[4] = connId & 0xff;
    f.set(data, 5);
    this.sendRaw(f);
  }
}

export const rpc = new Rpc();
