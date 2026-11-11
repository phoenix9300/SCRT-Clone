import { describe, expect, it } from "vitest";
import { CELL_SIZE, FG_BOLD, FG_INVERSE, LineAttr } from "./cell";
import { encodeKey } from "./keyboard";
import { encodeMouse } from "./mouse";
import { MouseEncoding, MouseMode, Terminal } from "./terminal";
import { getText } from "./text";

const mk = (o = {}) => new Terminal({ cols: 20, rows: 5, scrollback: 100, ...o });
const ks = { family: "xterm" as const, appCursor: false, appKeypad: false, newLineMode: false, backspaceIsBS: false, deleteIsDel: false, altIsMeta: true, vt52: false };
const key = (key: string, mods: Partial<{ ctrl: boolean; alt: boolean; shift: boolean }> = {}, code = "") => ({ key, code, ctrl: false, alt: false, shift: false, meta: false, ...mods });

describe("terminal core", () => {
  it("wraps, scrolls and keeps scrollback", () => {
    const t = mk();
    t.writeString("0123456789abcdefghijKLM\r\n");
    expect(t.screenText().slice(0, 2)).toEqual(["0123456789abcdefghij", "KLM"]);
    expect(t.buf.line(1).wrapped).toBe(true);
    for (let i = 0; i < 6; i++) t.writeString(`line${i}\r\n`);
    expect(t.normal.scrollbackLength).toBe(4);
    expect(t.rowText(3)).toBe("line5");
    // soft-wrapped rows are joined when copied
    const b = t.normal;
    expect(getText(b, { line: 0, col: 0 }, { line: 1, col: 19 }, false, 20)).toBe("0123456789abcdefghijKLM");
  });

  it("handles wide and combining characters", () => {
    const t = mk();
    t.writeString("a中b é");
    expect(t.rowText(0)).toBe("a中b é");
    expect(t.buf.x).toBe(6);
    expect(t.buf.line(0).width(1)).toBe(2);
    expect(t.buf.line(0).width(2)).toBe(0);
    // overwriting half of a wide character erases the other half
    t.writeString("\x1b[1;3HX");
    expect(t.rowText(0)).toBe("a Xb é");
  });

  it("applies SGR including colon sub-parameters", () => {
    const t = mk();
    t.writeString("\x1b[1;38:2::10:20:30;7mX\x1b[0mY");
    const d = t.buf.line(0).data;
    expect(d[1] & FG_BOLD).toBeTruthy();
    expect(d[1] & FG_INVERSE).toBeTruthy();
    expect(d[1] & 0xffffff).toBe(0x0a141e);
    expect(d[CELL_SIZE + 1]).toBe(0);
  });

  it("honours scroll regions, origin mode and the alternate screen", () => {
    const t = mk();
    t.writeString("A\r\nB\r\nC\r\nD\r\nE");
    t.writeString("\x1b[2;4r\x1b[4;1H\nX");
    expect(t.screenText()).toEqual(["A", "C", "D", "X", "E"]);
    t.writeString("\x1b[?6h\x1b[1;1HO\x1b[?6l\x1b[r");
    expect(t.rowText(1)).toBe("O");
    t.writeString("\x1b[?1049h\x1b[2JALT");
    expect(t.isAltScreen).toBe(true);
    t.writeString("\x1b[?1049l");
    expect(t.screenText()[0]).toBe("A");
  });

  it("answers device queries", () => {
    const t = mk();
    const out: string[] = [];
    t.onData = (s) => out.push(s);
    t.writeString("\x1b[3;4H\x1b[6n\x1b[c\x1b[>c\x1b[?2004h\x1b[?2004$p\x1bP$qm\x1b\\");
    expect(out).toEqual(["\x1b[3;4R", "\x1b[?64;1;2;6;9;15;18;21;22c", "\x1b[>41;354;0c", "\x1b[?2004;1$y", "\x1bP1$r0m\x1b\\"]);
  });

  it("supports DEC line drawing, double-width lines and titles", () => {
    const t = mk();
    let title = "";
    t.onTitle = (s) => (title = s);
    t.writeString("\x1b(0lqk\x1b(B x\x1b#6\x1b]2;my title\x07");
    expect(t.rowText(0)).toBe("┌─┐ x");
    expect(t.buf.line(0).lineAttr).toBe(LineAttr.DoubleWidth);
    expect(title).toBe("my title");
  });

  it("passes printer-controller data through", () => {
    const t = mk();
    let printed = "";
    t.onPrint = (s) => (printed += s);
    t.writeString("shown\x1b[5ihidden\r\nline\x1b[4i after");
    expect(printed).toBe("hidden\r\nline");
    expect(t.rowText(0)).toBe("shown after");
  });

  it("resizes without losing content", () => {
    const t = mk();
    for (let i = 0; i < 8; i++) t.writeString(`row${i}\r\n`);
    t.resize(30, 3);
    expect(t.rowText(1)).toBe("row7");
    t.resize(30, 6);
    expect(t.screenText().slice(0, 5)).toEqual(["row3", "row4", "row5", "row6", "row7"]);
  });

  it("speaks Wyse 50", () => {
    const t = mk({ emulation: "wyse50" });
    const out: string[] = [];
    t.onData = (s) => out.push(s);
    t.writeString("hello\x1b=\x22\x24X\x1bG4R\x1bG0\x1b \x1e!");
    expect(t.rowText(2)).toBe("    XR");
    expect(t.buf.line(2).data[5 * CELL_SIZE + 1] & FG_INVERSE).toBeTruthy();
    expect(t.rowText(0)).toBe("!ello");
    expect(out).toEqual(["50\r"]);
    t.writeString("\x1a");
    expect(t.screenText().join("")).toBe("");
    t.writeString("\x1bH2\x1bH:\x1bH3");
    expect(t.rowText(0)).toBe("┌─┐");
  });
});

describe("input encoding", () => {
  it("encodes keys per emulation and mode", () => {
    expect(encodeKey(key("ArrowUp"), ks)).toBe("\x1b[A");
    expect(encodeKey(key("ArrowUp"), { ...ks, appCursor: true })).toBe("\x1bOA");
    expect(encodeKey(key("ArrowUp", { ctrl: true }), ks)).toBe("\x1b[1;5A");
    expect(encodeKey(key("F5"), ks)).toBe("\x1b[15~");
    expect(encodeKey(key("F1"), { ...ks, family: "vt220" })).toBe("\x1bOP");
    expect(encodeKey(key("F1", { shift: true }), { ...ks, family: "vt220" })).toBe("\x1b[23~");
    expect(encodeKey(key("F2"), { ...ks, family: "sco" })).toBe("\x1b[N");
    expect(encodeKey(key("F1"), { ...ks, family: "linux" })).toBe("\x1b[[A");
    expect(encodeKey(key("F3"), { ...ks, family: "wyse" })).toBe("\x01B\r");
    expect(encodeKey(key("Home"), { ...ks, family: "vt220" })).toBe("\x1b[1~");
    expect(encodeKey(key("c", { ctrl: true }), ks)).toBe("\x03");
    expect(encodeKey(key("x", { alt: true }), ks)).toBe("\x1bx");
    expect(encodeKey(key("Backspace"), ks)).toBe("\x7f");
    expect(encodeKey(key("Backspace"), { ...ks, backspaceIsBS: true })).toBe("\x08");
    expect(encodeKey(key("Tab", { shift: true }), ks)).toBe("\x1b[Z");
    expect(encodeKey(key("5", {}, "Numpad5"), { ...ks, appKeypad: true })).toBe("\x1bOu");
    expect(encodeKey(key("a"), ks)).toBeNull();
  });

  it("encodes mouse reports", () => {
    const e = { button: 0, kind: "down" as const, col: 9, row: 4, px: 0, py: 0, shift: false, alt: false, ctrl: false };
    expect(encodeMouse(e, MouseMode.Normal, MouseEncoding.Default)).toBe("\x1b[M *%");
    expect(encodeMouse(e, MouseMode.Normal, MouseEncoding.Sgr)).toBe("\x1b[<0;10;5M");
    expect(encodeMouse({ ...e, kind: "up" }, MouseMode.Normal, MouseEncoding.Sgr)).toBe("\x1b[<0;10;5m");
    expect(encodeMouse({ ...e, kind: "move" }, MouseMode.Normal, MouseEncoding.Sgr)).toBeNull();
    expect(encodeMouse(e, MouseMode.None, MouseEncoding.Sgr)).toBeNull();
  });
});
