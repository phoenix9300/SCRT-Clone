// Escape sequence parser modelled on the DEC VT500 state machine
// (https://vt100.net/emu/dec_ansi_parser). It consumes decoded code points.

export const MAX_PARAMS = 32;
const MAX_STRING = 8 * 1024 * 1024;

/** Numeric parameters of a CSI / DCS sequence. A value of -1 means "omitted". */
export class Params {
  readonly values = new Int32Array(MAX_PARAMS);
  /** sub[i] === 1 when values[i] was introduced with ':' (a sub-parameter) */
  readonly sub = new Uint8Array(MAX_PARAMS);
  length = 0;

  reset(): void {
    this.length = 0;
  }

  /** Value of parameter i, or `def` when omitted or zero-defaulted. */
  get(i: number, def: number): number {
    if (i >= this.length) return def;
    const v = this.values[i];
    return v < 0 ? def : v;
  }

  /** Like get(), but treats 0 as omitted (most cursor movement counts). */
  getNonZero(i: number, def: number): number {
    if (i >= this.length) return def;
    const v = this.values[i];
    return v <= 0 ? def : v;
  }

  toArray(): number[] {
    return Array.from(this.values.subarray(0, this.length));
  }
}

export interface ParserHandler {
  /** Printable code points data[start..end) */
  print(data: Uint32Array, start: number, end: number): void;
  /** C0 / C1 control */
  execute(code: number): void;
  /** ESC sequence; `collect` holds up to two intermediate bytes (first<<8|second) */
  escDispatch(collect: number, final: number): void;
  /** CSI sequence; prefix is one of 0, '<', '=', '>', '?' */
  csiDispatch(prefix: number, collect: number, final: number, params: Params): void;
  oscDispatch(data: string): void;
  dcsDispatch(prefix: number, collect: number, final: number, params: Params, data: string): void;
  /** SOS / PM / APC strings (kind is the introducer: 'X', '^' or '_') */
  stringDispatch(kind: number, data: string): void;
}

const enum S {
  Ground,
  Escape,
  EscapeIntermediate,
  CsiEntry,
  CsiParam,
  CsiIntermediate,
  CsiIgnore,
  DcsEntry,
  DcsParam,
  DcsIntermediate,
  DcsPassthrough,
  DcsIgnore,
  OscString,
  SosPmApcString,
  /** raw capture of N code points for the handler (VT52 ESC Y, Linux ESC ] P) */
  Capture,
}

export class Parser {
  private state: S = S.Ground;
  private collect = 0;
  private prefix = 0;
  private readonly params = new Params();
  private paramStarted = false;
  private str: string[] = [];
  private strLen = 0;
  private strKind = 0;
  /** set while a string state saw ESC and waits for '\' */
  private strEsc = false;
  private dcsFinal = 0;
  private captureLeft = 0;
  private captureBuf: number[] = [];
  private captureCb: ((codes: number[]) => void) | null = null;

  /** When true, code points 0x80-0x9f are interpreted as C1 controls. */
  c1 = false;
  /** VT52 mode: ESC sequences are dispatched immediately without CSI etc. */
  vt52 = false;
  /** Linux console: recognise the unterminated "ESC ] P nrrggbb" palette sequence. */
  linuxOsc = false;

  constructor(private readonly h: ParserHandler) {}

  reset(): void {
    this.state = S.Ground;
    this.collect = 0;
    this.prefix = 0;
    this.params.reset();
    this.str = [];
    this.strLen = 0;
    this.strEsc = false;
    this.captureCb = null;
  }

  /** Ask the parser to hand the next n code points to cb verbatim. */
  captureNext(n: number, cb: (codes: number[]) => void): void {
    this.captureLeft = n;
    this.captureBuf = [];
    this.captureCb = cb;
    this.state = S.Capture;
  }

  private clear(): void {
    this.collect = 0;
    this.prefix = 0;
    this.params.reset();
    this.paramStarted = false;
  }

  private pushParamDigit(d: number): void {
    const p = this.params;
    if (!this.paramStarted) {
      if (p.length >= MAX_PARAMS) return;
      p.values[p.length] = 0;
      p.sub[p.length] = 0;
      p.length++;
      this.paramStarted = true;
    } else if (p.values[p.length - 1] < 0) {
      p.values[p.length - 1] = 0;
    }
    const i = p.length - 1;
    const v = p.values[i] * 10 + d;
    p.values[i] = v > 0x7fffff ? 0x7fffff : v;
  }

  private paramSeparator(isSub: boolean): void {
    const p = this.params;
    if (!this.paramStarted) {
      // leading separator: first parameter omitted
      if (p.length < MAX_PARAMS) {
        p.values[p.length] = -1;
        p.sub[p.length] = 0;
        p.length++;
      }
    }
    if (p.length < MAX_PARAMS) {
      p.values[p.length] = -1;
      p.sub[p.length] = isSub ? 1 : 0;
      p.length++;
    }
    this.paramStarted = true;
  }

  private strStart(kind: number): void {
    this.str = [];
    this.strLen = 0;
    this.strKind = kind;
    this.strEsc = false;
  }

  private strPut(code: number): void {
    if (this.strLen < MAX_STRING) {
      this.str.push(code > 0xffff ? String.fromCodePoint(code) : String.fromCharCode(code));
      this.strLen++;
    }
  }

  private strEnd(ok: boolean): void {
    const data = this.str.join("");
    this.str = [];
    this.strLen = 0;
    const st = this.state;
    this.state = S.Ground;
    if (!ok) return;
    if (st === S.OscString) this.h.oscDispatch(data);
    else if (st === S.DcsPassthrough) this.h.dcsDispatch(this.prefix, this.collect, this.dcsFinal, this.params, data);
    else if (st === S.SosPmApcString) this.h.stringDispatch(this.strKind, data);
  }

  parse(data: Uint32Array, length: number): void {
    const h = this.h;
    let i = 0;
    while (i < length) {
      let code = data[i];

      // ---- fast path: runs of printable characters in ground state
      if (this.state === S.Ground) {
        if (code >= 0x20 && code !== 0x7f && !(code >= 0x80 && code <= 0x9f)) {
          let j = i + 1;
          while (j < length) {
            const c = data[j];
            if (c < 0x20 || c === 0x7f || (c >= 0x80 && c <= 0x9f)) break;
            j++;
          }
          h.print(data, i, j);
          i = j;
          continue;
        }
      }
      i++;

      if (this.state === S.Capture) {
        this.captureBuf.push(code);
        if (--this.captureLeft <= 0) {
          const cb = this.captureCb;
          const buf = this.captureBuf;
          this.captureCb = null;
          this.state = S.Ground;
          cb?.(buf);
        }
        continue;
      }

      // ---- string states (OSC / DCS / SOS / PM / APC)
      if (this.state >= S.DcsPassthrough) {
        if (this.strEsc) {
          this.strEsc = false;
          if (code === 0x5c) {
            this.strEnd(true); // ESC \ = ST
            continue;
          }
          // any other ESC sequence aborts the string and starts a new sequence
          this.strEnd(false);
          this.state = S.Escape;
          this.clear();
          // fall through to normal processing of `code` in Escape state
        } else {
          if (code === 0x1b) {
            this.strEsc = true;
            continue;
          }
          if (code === 0x07 && this.state === S.OscString) {
            this.strEnd(true);
            continue;
          }
          if (code === 0x9c && this.c1) {
            this.strEnd(true);
            continue;
          }
          if (code === 0x18 || code === 0x1a) {
            this.strEnd(false);
            if (code === 0x1a) h.execute(code);
            continue;
          }
          if (this.state === S.DcsIgnore) continue;
          if (this.state === S.OscString) {
            if (code >= 0x20) this.strPut(code);
            // Linux console palette: ESC ] P n rr gg bb (no terminator)
            if (this.linuxOsc && this.strLen === 1 && this.str[0] === "P") {
              this.str = [];
              this.strLen = 0;
              this.state = S.Ground;
              this.captureNext(7, (codes) => h.oscDispatch("linuxP;" + String.fromCodePoint(...codes)));
            } else if (this.linuxOsc && this.strLen === 1 && this.str[0] === "R") {
              this.str = [];
              this.strLen = 0;
              this.state = S.Ground;
              h.oscDispatch("linuxR");
            }
            continue;
          }
          if (this.state === S.DcsPassthrough) {
            if (code !== 0x7f) this.strPut(code);
            continue;
          }
          // SOS / PM / APC
          this.strPut(code);
          continue;
        }
      }

      // ---- "anywhere" transitions
      if (code === 0x1b) {
        this.state = S.Escape;
        this.clear();
        continue;
      }
      if (code === 0x18 || code === 0x1a) {
        this.state = S.Ground;
        if (code === 0x1a) h.execute(code);
        continue;
      }
      if (code >= 0x80 && code <= 0x9f) {
        if (!this.c1) {
          // not interpreting C1: ignore inside sequences, drop in ground
          continue;
        }
        switch (code) {
          case 0x90:
            this.clear();
            this.state = S.DcsEntry;
            break;
          case 0x9b:
            this.clear();
            this.state = S.CsiEntry;
            break;
          case 0x9d:
            this.strStart(0x5d);
            this.state = S.OscString;
            break;
          case 0x98:
          case 0x9e:
          case 0x9f:
            this.strStart(code === 0x98 ? 0x58 : code === 0x9e ? 0x5e : 0x5f);
            this.state = S.SosPmApcString;
            break;
          case 0x9c:
            this.state = S.Ground;
            break;
          default:
            this.state = S.Ground;
            h.execute(code);
        }
        continue;
      }

      switch (this.state) {
        case S.Ground:
          // only controls reach this point
          if (code < 0x20) h.execute(code);
          break;

        case S.Escape:
          if (code < 0x20) {
            h.execute(code);
          } else if (code === 0x7f) {
            /* ignore */
          } else if (this.vt52) {
            this.state = S.Ground;
            h.escDispatch(0, code);
          } else if (code < 0x30) {
            this.collect = code;
            this.state = S.EscapeIntermediate;
          } else if (code === 0x5b) {
            this.clear();
            this.state = S.CsiEntry;
          } else if (code === 0x5d) {
            this.strStart(0x5d);
            this.state = S.OscString;
          } else if (code === 0x50) {
            this.clear();
            this.state = S.DcsEntry;
          } else if (code === 0x58 || code === 0x5e || code === 0x5f) {
            this.strStart(code);
            this.state = S.SosPmApcString;
          } else if (code < 0x7f) {
            this.state = S.Ground;
            h.escDispatch(0, code);
          } else {
            this.state = S.Ground;
          }
          break;

        case S.EscapeIntermediate:
          if (code < 0x20) h.execute(code);
          else if (code < 0x30) this.collect = ((this.collect << 8) | code) & 0xffff;
          else if (code < 0x7f) {
            this.state = S.Ground;
            h.escDispatch(this.collect, code);
          } else if (code > 0x7f) this.state = S.Ground;
          break;

        case S.CsiEntry:
        case S.CsiParam:
          if (code < 0x20) h.execute(code);
          else if (code >= 0x30 && code <= 0x39) {
            this.pushParamDigit(code - 0x30);
            this.state = S.CsiParam;
          } else if (code === 0x3b) {
            this.paramSeparator(false);
            this.state = S.CsiParam;
          } else if (code === 0x3a) {
            this.paramSeparator(true);
            this.state = S.CsiParam;
          } else if (code >= 0x3c && code <= 0x3f) {
            if (this.state === S.CsiEntry) {
              this.prefix = code;
              this.state = S.CsiParam;
            } else this.state = S.CsiIgnore;
          } else if (code >= 0x20 && code <= 0x2f) {
            this.collect = code;
            this.state = S.CsiIntermediate;
          } else if (code >= 0x40 && code <= 0x7e) {
            this.state = S.Ground;
            h.csiDispatch(this.prefix, this.collect, code, this.params);
          } else if (code > 0x7f) this.state = S.Ground;
          break;

        case S.CsiIntermediate:
          if (code < 0x20) h.execute(code);
          else if (code <= 0x2f) this.collect = ((this.collect << 8) | code) & 0xffff;
          else if (code >= 0x40 && code <= 0x7e) {
            this.state = S.Ground;
            h.csiDispatch(this.prefix, this.collect, code, this.params);
          } else if (code >= 0x30 && code <= 0x3f) this.state = S.CsiIgnore;
          else if (code > 0x7f) this.state = S.Ground;
          break;

        case S.CsiIgnore:
          if (code < 0x20) h.execute(code);
          else if (code >= 0x40 && code <= 0x7e) this.state = S.Ground;
          break;

        case S.DcsEntry:
        case S.DcsParam:
          if (code < 0x20) {
            /* ignore */
          } else if (code >= 0x30 && code <= 0x39) {
            this.pushParamDigit(code - 0x30);
            this.state = S.DcsParam;
          } else if (code === 0x3b) {
            this.paramSeparator(false);
            this.state = S.DcsParam;
          } else if (code === 0x3a) {
            this.state = S.DcsIgnore;
            this.strStart(0);
          } else if (code >= 0x3c && code <= 0x3f) {
            if (this.state === S.DcsEntry) {
              this.prefix = code;
              this.state = S.DcsParam;
            } else {
              this.state = S.DcsIgnore;
              this.strStart(0);
            }
          } else if (code >= 0x20 && code <= 0x2f) {
            this.collect = code;
            this.state = S.DcsIntermediate;
          } else if (code >= 0x40 && code <= 0x7e) {
            this.dcsFinal = code;
            this.strStart(0);
            this.state = S.DcsPassthrough;
          }
          break;

        case S.DcsIntermediate:
          if (code < 0x20) {
            /* ignore */
          } else if (code <= 0x2f) this.collect = ((this.collect << 8) | code) & 0xffff;
          else if (code >= 0x40 && code <= 0x7e) {
            this.dcsFinal = code;
            this.strStart(0);
            this.state = S.DcsPassthrough;
          } else if (code >= 0x30 && code <= 0x3f) {
            this.state = S.DcsIgnore;
            this.strStart(0);
          }
          break;
      }
    }
  }
}
