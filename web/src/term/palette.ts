// Colour schemes and the 256-colour palette.

export interface ColorScheme {
  id: string;
  name: string;
  dark: boolean;
  foreground: string;
  background: string;
  cursor: string;
  /** selection background */
  selection: string;
  /** 16 ANSI colours: black, red, green, yellow, blue, magenta, cyan, white + bright variants */
  ansi: string[];
}

// prettier-ignore
export const COLOR_SCHEMES: ColorScheme[] = [
  { id: "hawser-dark", name: "Hawser Dark", dark: true, foreground: "#d5dae3", background: "#14171c", cursor: "#5fd4c4", selection: "#2f4a5c",
    ansi: ["#1f242c", "#e8646a", "#7fc878", "#e2b55a", "#5aa2f0", "#c58af0", "#4cc7c0", "#c3c9d4", "#5b6577", "#ff8e8f", "#a3e09a", "#f5d27e", "#86bdff", "#dcaeff", "#7fe3da", "#f2f4f8"] },
  { id: "hawser-light", name: "Hawser Light", dark: false, foreground: "#232831", background: "#fbfbf9", cursor: "#0e8f84", selection: "#c5dcef",
    ansi: ["#232831", "#c4353f", "#2e8540", "#a86a00", "#1f66c7", "#8a3fc0", "#0b7f86", "#8d95a3", "#5a6372", "#e04a52", "#3f9e52", "#c58412", "#3d83e6", "#a45adb", "#1d9aa3", "#f4f5f7"] },
  { id: "traditional", name: "Traditional (white on black)", dark: true, foreground: "#c0c0c0", background: "#000000", cursor: "#c0c0c0", selection: "#3a5f8f",
    ansi: ["#000000", "#aa0000", "#00aa00", "#aa5500", "#0000aa", "#aa00aa", "#00aaaa", "#aaaaaa", "#555555", "#ff5555", "#55ff55", "#ffff55", "#5555ff", "#ff55ff", "#55ffff", "#ffffff"] },
  { id: "black-on-white", name: "Black on White", dark: false, foreground: "#000000", background: "#ffffff", cursor: "#000000", selection: "#b5d5ff",
    ansi: ["#000000", "#aa0000", "#008a00", "#8a5a00", "#0000aa", "#aa00aa", "#008a8a", "#8a8a8a", "#555555", "#e00000", "#00b000", "#b08000", "#3030ff", "#d000d0", "#00a8a8", "#ffffff"] },
  { id: "green-phosphor", name: "Green Phosphor", dark: true, foreground: "#33ff66", background: "#041208", cursor: "#66ff99", selection: "#145c2a",
    ansi: ["#041208", "#1f9c44", "#33ff66", "#7dffa0", "#22b84f", "#2bd95c", "#55ff88", "#b6ffca", "#136b2f", "#3fd96b", "#66ff8f", "#a8ffc0", "#3be06e", "#5cf589", "#8affb0", "#e0ffe9"] },
  { id: "amber", name: "Amber Phosphor", dark: true, foreground: "#ffb52e", background: "#140d02", cursor: "#ffd27a", selection: "#5c3f0a",
    ansi: ["#140d02", "#b3731a", "#ffb52e", "#ffd27a", "#cc8a1f", "#e69c24", "#ffc24f", "#ffe1a6", "#6b4710", "#d98f26", "#ffc04a", "#ffdc94", "#e6a233", "#f5b340", "#ffcf70", "#fff0d1"] },
  { id: "tango-dark", name: "Tango Dark", dark: true, foreground: "#d3d7cf", background: "#2e3436", cursor: "#d3d7cf", selection: "#555753",
    ansi: ["#2e3436", "#cc0000", "#4e9a06", "#c4a000", "#3465a4", "#75507b", "#06989a", "#d3d7cf", "#555753", "#ef2929", "#8ae234", "#fce94f", "#729fcf", "#ad7fa8", "#34e2e2", "#eeeeec"] },
  { id: "solarized-dark", name: "Solarized Dark", dark: true, foreground: "#839496", background: "#002b36", cursor: "#93a1a1", selection: "#073642",
    ansi: ["#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5", "#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3"] },
  { id: "solarized-light", name: "Solarized Light", dark: false, foreground: "#657b83", background: "#fdf6e3", cursor: "#586e75", selection: "#eee8d5",
    ansi: ["#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5", "#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3"] },
  { id: "dracula", name: "Dracula", dark: true, foreground: "#f8f8f2", background: "#282a36", cursor: "#f8f8f2", selection: "#44475a",
    ansi: ["#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2", "#6272a4", "#ff6e6e", "#69ff94", "#ffffa5", "#d6acff", "#ff92df", "#a4ffff", "#ffffff"] },
  { id: "nord", name: "Nord", dark: true, foreground: "#d8dee9", background: "#2e3440", cursor: "#d8dee9", selection: "#434c5e",
    ansi: ["#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0", "#4c566a", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#8fbcbb", "#eceff4"] },
  { id: "gruvbox-dark", name: "Gruvbox Dark", dark: true, foreground: "#ebdbb2", background: "#282828", cursor: "#ebdbb2", selection: "#504945",
    ansi: ["#282828", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#a89984", "#928374", "#fb4934", "#b8bb26", "#fabd2f", "#83a598", "#d3869b", "#8ec07c", "#ebdbb2"] },
  { id: "one-dark", name: "One Dark", dark: true, foreground: "#abb2bf", background: "#282c34", cursor: "#528bff", selection: "#3e4451",
    ansi: ["#282c34", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#abb2bf", "#5c6370", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#ffffff"] },
  { id: "monokai", name: "Monokai", dark: true, foreground: "#f8f8f2", background: "#272822", cursor: "#f8f8f0", selection: "#49483e",
    ansi: ["#272822", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f8f8f2", "#75715e", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f9f8f5"] },
  { id: "white-on-blue", name: "White on Blue", dark: true, foreground: "#e8e8e8", background: "#0a2a6b", cursor: "#ffffff", selection: "#3d62b0",
    ansi: ["#000000", "#d04040", "#3fbf3f", "#d0b030", "#6f9fff", "#d060d0", "#30c0c0", "#c8c8c8", "#6a7fa8", "#ff7070", "#70ff70", "#ffff70", "#9fbfff", "#ff8cff", "#70ffff", "#ffffff"] },
];

export function schemeById(id: string | undefined, custom: ColorScheme[] = []): ColorScheme {
  return custom.find((s) => s.id === id) ?? COLOR_SCHEMES.find((s) => s.id === id) ?? COLOR_SCHEMES[0];
}

export function hexToRgb(hex: string): number {
  let h = hex.trim().replace(/^#/, "");
  if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
  const n = parseInt(h.slice(0, 6), 16);
  return isNaN(n) ? 0 : n;
}

export function rgbToHex(c: number): string {
  return "#" + (c & 0xffffff).toString(16).padStart(6, "0");
}

/** Resolved colours used by the renderer (all 0xRRGGBB numbers). */
export interface Theme {
  foreground: number;
  background: number;
  cursor: number;
  selection: number;
  palette: Uint32Array; // 256 entries
}

const CUBE = [0, 0x5f, 0x87, 0xaf, 0xd7, 0xff];

export function buildTheme(scheme: ColorScheme): Theme {
  const palette = new Uint32Array(256);
  for (let i = 0; i < 16; i++) palette[i] = hexToRgb(scheme.ansi[i] ?? "#000000");
  for (let i = 0; i < 216; i++) {
    const r = CUBE[Math.floor(i / 36)];
    const g = CUBE[Math.floor(i / 6) % 6];
    const b = CUBE[i % 6];
    palette[16 + i] = (r << 16) | (g << 8) | b;
  }
  for (let i = 0; i < 24; i++) {
    const v = 8 + i * 10;
    palette[232 + i] = (v << 16) | (v << 8) | v;
  }
  return {
    foreground: hexToRgb(scheme.foreground),
    background: hexToRgb(scheme.background),
    cursor: hexToRgb(scheme.cursor),
    selection: hexToRgb(scheme.selection),
    palette,
  };
}

/** Blend colour a over b with the given alpha (0..1). */
export function blend(a: number, b: number, alpha: number): number {
  const r = Math.round(((a >> 16) & 0xff) * alpha + ((b >> 16) & 0xff) * (1 - alpha));
  const g = Math.round(((a >> 8) & 0xff) * alpha + ((b >> 8) & 0xff) * (1 - alpha));
  const bl = Math.round((a & 0xff) * alpha + (b & 0xff) * (1 - alpha));
  return (r << 16) | (g << 8) | bl;
}

export function luminance(c: number): number {
  const f = (v: number) => {
    v /= 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * f((c >> 16) & 0xff) + 0.7152 * f((c >> 8) & 0xff) + 0.0722 * f(c & 0xff);
}

const cssCache = new Map<number, string>();
export function css(c: number): string {
  let s = cssCache.get(c);
  if (!s) {
    s = "#" + (c & 0xffffff).toString(16).padStart(6, "0");
    if (cssCache.size > 4096) cssCache.clear();
    cssCache.set(c, s);
  }
  return s;
}
