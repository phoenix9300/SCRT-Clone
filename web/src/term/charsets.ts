// Character-set designation (SCS) tables and legacy byte decoders.

/** A 94-character set maps code points 0x21..0x7e; undefined = identity. */
export type Charset = { [ch: number]: number } | null;

function table(map: Record<string, string>): Charset {
  const t: { [ch: number]: number } = {};
  for (const k of Object.keys(map)) t[k.charCodeAt(0)] = map[k].codePointAt(0)!;
  return t;
}

/** DEC Special Graphics and Line Drawing (ESC ( 0). */
export const DEC_SPECIAL: Charset = table({
  "_": " ",
  "`": "◆", a: "▒", b: "␉", c: "␌", d: "␍", e: "␊",
  f: "°", g: "±", h: "␤", i: "␋", j: "┘", k: "┐",
  l: "┌", m: "└", n: "┼", o: "⎺", p: "⎻", q: "─",
  r: "⎼", s: "⎽", t: "├", u: "┤", v: "┴", w: "┬",
  x: "│", y: "≤", z: "≥", "{": "π", "|": "≠", "}": "£",
  "~": "·",
});

const UK = table({ "#": "£" });
const DUTCH = table({ "#": "£", "@": "¾", "[": "ĳ", "\\": "½", "]": "|", "{": "¨", "|": "f", "}": "¼", "~": "´" });
const FINNISH = table({ "[": "Ä", "\\": "Ö", "]": "Å", "^": "Ü", "`": "é", "{": "ä", "|": "ö", "}": "å", "~": "ü" });
const FRENCH = table({ "#": "£", "@": "à", "[": "°", "\\": "ç", "]": "§", "{": "é", "|": "ù", "}": "è", "~": "¨" });
const FRENCH_CANADIAN = table({ "@": "à", "[": "â", "\\": "ç", "]": "ê", "^": "î", "`": "ô", "{": "é", "|": "ù", "}": "è", "~": "û" });
const GERMAN = table({ "@": "§", "[": "Ä", "\\": "Ö", "]": "Ü", "{": "ä", "|": "ö", "}": "ü", "~": "ß" });
const ITALIAN = table({ "#": "£", "@": "§", "[": "°", "\\": "ç", "]": "é", "`": "ù", "{": "à", "|": "ò", "}": "è", "~": "ì" });
const NORWEGIAN = table({ "@": "Ä", "[": "Æ", "\\": "Ø", "]": "Å", "^": "Ü", "`": "ä", "{": "æ", "|": "ø", "}": "å", "~": "ü" });
const SPANISH = table({ "#": "£", "@": "§", "[": "¡", "\\": "Ñ", "]": "¿", "{": "°", "|": "ñ", "}": "ç" });
const SWEDISH = table({ "@": "É", "[": "Ä", "\\": "Ö", "]": "Å", "^": "Ü", "`": "é", "{": "ä", "|": "ö", "}": "å", "~": "ü" });
const SWISS = table({ "#": "ù", "@": "à", "[": "é", "\\": "ç", "]": "ê", "^": "î", "_": "è", "`": "ô", "{": "ä", "|": "ö", "}": "ü", "~": "û" });

/** Look up a 94-set by its SCS final character. Unknown sets map to ASCII. */
export function charsetFor(final: string): Charset {
  switch (final) {
    case "0":
    case "2":
      return DEC_SPECIAL;
    case "A":
      return UK;
    case "4":
      return DUTCH;
    case "C":
    case "5":
      return FINNISH;
    case "R":
    case "f":
      return FRENCH;
    case "Q":
    case "9":
      return FRENCH_CANADIAN;
    case "K":
      return GERMAN;
    case "Y":
      return ITALIAN;
    case "E":
    case "6":
    case "`":
      return NORWEGIAN;
    case "Z":
      return SPANISH;
    case "H":
    case "7":
      return SWEDISH;
    case "=":
      return SWISS;
    default:
      return null; // "B" (US-ASCII) and anything unsupported
  }
}

// prettier-ignore
const CP437_HIGH =
  "ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀" +
  "αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ ";
// prettier-ignore
const CP437_LOW = "\u0000☺☻♥♦♣♠•◘○◙♂♀♪♫☼►◄↕‼¶§▬↨↑↓→←∟↔▲▼";
// prettier-ignore
const CP850_HIGH =
  "ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜø£Ø×ƒáíóúñÑªº¿®¬½¼¡«»░▒▓│┤ÁÂÀ©╣║╗╝¢¥┐└┴┬├─┼ãÃ╚╔╩╦╠═╬¤ðÐÊËÈıÍÎÏ┘┌█▄¦Ì▀" +
  "ÓßÔÒõÕµþÞÚÛÙýÝ¯´­±‗¾¶§÷¸°¨·¹³²■ ";

export function cp437(code: number): number {
  if (code >= 0x80 && code <= 0xff) return CP437_HIGH.codePointAt(code - 0x80)!;
  return code;
}

/** CP437 glyph for a C0 code (used by the "null mapping" / SCO alternate font). */
export function cp437Low(code: number): number {
  return code > 0 && code < 0x20 ? CP437_LOW.codePointAt(code)! : code;
}

/** Streaming decoder from bytes to code points. */
export interface Decoder {
  /** Decodes `input` into `out` (which must hold at least input.length + 4 entries) and returns the count. */
  decode(input: Uint8Array, out: Uint32Array): number;
  reset(): void;
}

class Utf8Decoder implements Decoder {
  private need = 0;
  private cp = 0;
  private min = 0;

  reset(): void {
    this.need = 0;
  }

  decode(input: Uint8Array, out: Uint32Array): number {
    let n = 0;
    let need = this.need;
    let cp = this.cp;
    let min = this.min;
    const len = input.length;
    let i = 0;
    while (i < len) {
      const b = input[i++];
      if (need === 0) {
        if (b < 0x80) {
          out[n++] = b;
          // fast path for ASCII runs
          while (i < len && input[i] < 0x80) out[n++] = input[i++];
        } else if (b >= 0xc2 && b <= 0xdf) {
          need = 1;
          cp = b & 0x1f;
          min = 0x80;
        } else if (b >= 0xe0 && b <= 0xef) {
          need = 2;
          cp = b & 0x0f;
          min = 0x800;
        } else if (b >= 0xf0 && b <= 0xf4) {
          need = 3;
          cp = b & 0x07;
          min = 0x10000;
        } else {
          out[n++] = 0xfffd;
        }
      } else if ((b & 0xc0) === 0x80) {
        cp = (cp << 6) | (b & 0x3f);
        if (--need === 0) {
          if (cp < min || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) out[n++] = 0xfffd;
          else out[n++] = cp;
        }
      } else {
        // truncated sequence: emit replacement and reprocess this byte
        out[n++] = 0xfffd;
        need = 0;
        i--;
      }
    }
    this.need = need;
    this.cp = cp;
    this.min = min;
    return n;
  }
}

class Latin1Decoder implements Decoder {
  reset(): void {}
  decode(input: Uint8Array, out: Uint32Array): number {
    for (let i = 0; i < input.length; i++) out[i] = input[i];
    return input.length;
  }
}

class TableDecoder implements Decoder {
  private readonly high: Uint32Array;
  constructor(high: string) {
    this.high = new Uint32Array(128);
    let i = 0;
    for (const ch of high) this.high[i++] = ch.codePointAt(0)!;
  }
  reset(): void {}
  decode(input: Uint8Array, out: Uint32Array): number {
    for (let i = 0; i < input.length; i++) {
      const b = input[i];
      out[i] = b < 0x80 ? b : this.high[b - 0x80];
    }
    return input.length;
  }
}

/** Uses the platform TextDecoder for every other encoding. */
class PlatformDecoder implements Decoder {
  private td: TextDecoder;
  constructor(private readonly label: string) {
    this.td = new TextDecoder(label);
  }
  reset(): void {
    this.td = new TextDecoder(this.label);
  }
  decode(input: Uint8Array, out: Uint32Array): number {
    const s = this.td.decode(input, { stream: true });
    let n = 0;
    for (let i = 0; i < s.length; i++) {
      let c = s.charCodeAt(i);
      if (c >= 0xd800 && c < 0xdc00 && i + 1 < s.length) {
        const d = s.charCodeAt(i + 1);
        if (d >= 0xdc00 && d < 0xe000) {
          c = 0x10000 + ((c - 0xd800) << 10) + (d - 0xdc00);
          i++;
        }
      }
      if (n < out.length) out[n++] = c;
    }
    return n;
  }
}

/** Encodings offered in the session options, in display order. */
export const ENCODINGS: { id: string; label: string }[] = [
  { id: "utf-8", label: "UTF-8" },
  { id: "iso-8859-1", label: "ISO-8859-1 (Latin 1)" },
  { id: "iso-8859-2", label: "ISO-8859-2 (Central European)" },
  { id: "iso-8859-5", label: "ISO-8859-5 (Cyrillic)" },
  { id: "iso-8859-7", label: "ISO-8859-7 (Greek)" },
  { id: "iso-8859-9", label: "ISO-8859-9 (Turkish)" },
  { id: "iso-8859-15", label: "ISO-8859-15 (Latin 9)" },
  { id: "windows-1250", label: "Windows-1250" },
  { id: "windows-1251", label: "Windows-1251 (Cyrillic)" },
  { id: "windows-1252", label: "Windows-1252" },
  { id: "koi8-r", label: "KOI8-R" },
  { id: "koi8-u", label: "KOI8-U" },
  { id: "cp437", label: "OEM 437 (DOS / line drawing)" },
  { id: "cp850", label: "OEM 850 (DOS Latin 1)" },
  { id: "ibm866", label: "OEM 866 (DOS Cyrillic)" },
  { id: "shift_jis", label: "Shift-JIS" },
  { id: "euc-jp", label: "EUC-JP" },
  { id: "euc-kr", label: "EUC-KR" },
  { id: "gbk", label: "GBK" },
  { id: "gb18030", label: "GB18030" },
  { id: "big5", label: "Big5" },
];

export function createDecoder(encoding: string): Decoder {
  const e = (encoding || "utf-8").toLowerCase();
  switch (e) {
    case "utf-8":
    case "utf8":
      return new Utf8Decoder();
    case "iso-8859-1":
    case "latin1":
      return new Latin1Decoder();
    case "cp437":
    case "ibm437":
      return new TableDecoder(CP437_HIGH);
    case "cp850":
    case "ibm850":
      return new TableDecoder(CP850_HIGH);
    default:
      try {
        return new PlatformDecoder(e);
      } catch {
        return new Utf8Decoder();
      }
  }
}

const encodeTables = new Map<string, Map<number, number>>();

function reverseTable(high: string): Map<number, number> {
  const m = new Map<number, number>();
  let i = 0;
  for (const ch of high) m.set(ch.codePointAt(0)!, 0x80 + i++);
  return m;
}

function singleByteTable(encoding: string): Map<number, number> | null {
  let t = encodeTables.get(encoding);
  if (t) return t;
  if (encoding === "cp437" || encoding === "ibm437") t = reverseTable(CP437_HIGH);
  else if (encoding === "cp850" || encoding === "ibm850") t = reverseTable(CP850_HIGH);
  else {
    // derive the reverse table from the platform decoder (single-byte sets only)
    try {
      const td = new TextDecoder(encoding);
      const bytes = new Uint8Array(128);
      for (let i = 0; i < 128; i++) bytes[i] = 0x80 + i;
      const s = td.decode(bytes);
      if (s.length !== 128) return null;
      t = new Map();
      for (let i = 0; i < 128; i++) t.set(s.charCodeAt(i), 0x80 + i);
    } catch {
      return null;
    }
  }
  encodeTables.set(encoding, t);
  return t;
}

const utf8Encoder = new TextEncoder();

/** Encode keyboard / paste text for the wire in the session's encoding. */
export function encodeText(s: string, encoding: string): Uint8Array {
  const e = (encoding || "utf-8").toLowerCase();
  if (e === "utf-8" || e === "utf8") return utf8Encoder.encode(s);
  if (e === "iso-8859-1" || e === "latin1") {
    const out = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i);
      out[i] = c < 0x100 ? c : 0x3f;
    }
    return out;
  }
  const t = singleByteTable(e);
  if (t) {
    const out = new Uint8Array(s.length);
    let n = 0;
    for (const ch of s) {
      const c = ch.codePointAt(0)!;
      out[n++] = c < 0x80 ? c : (t.get(c) ?? 0x3f);
    }
    return out.subarray(0, n);
  }
  // multi-byte legacy encodings cannot be produced by browsers; fall back to UTF-8
  return utf8Encoder.encode(s);
}
