// Pixel-exact drawing of box-drawing, block-element, braille and powerline
// characters so lines join seamlessly regardless of the font.

// Arm weights "LRUD" for U+2500..U+257F: 0 none, 1 light, 2 heavy, 3 double.
// Dashed, arc and diagonal glyphs are handled separately ("----").
// prettier-ignore
const BOX =
  "1100 2200 0011 0022 ---- ---- ---- ---- ---- ---- ---- ---- 0101 0201 0102 0202 " +
  "1001 2001 1002 2002 0110 0210 0120 0220 1010 2010 1020 2020 0111 0211 0121 0112 " +
  "0122 0221 0212 0222 1011 2011 1021 1012 1022 2021 2012 2022 1101 2101 1201 2201 " +
  "1102 2102 1202 2202 1110 2110 1210 2210 1120 2120 1220 2220 1111 2111 1211 2211 " +
  "1121 1112 1122 2121 1221 2112 1212 2221 2212 2122 1222 2222 ---- ---- ---- ---- " +
  "3300 0033 0301 0103 0303 3001 1003 3003 0310 0130 0330 3010 1030 3030 0311 0133 " +
  "0333 3011 1033 3033 3301 1103 3303 3310 1130 3330 3311 1133 3333 ---- ---- ---- " +
  "---- ---- ---- ---- 1000 0010 0100 0001 2000 0020 0200 0002 1200 0012 2100 0021";
const BOX_ARMS = BOX.split(" ");

export function isSpecialGlyph(code: number): boolean {
  return (code >= 0x2500 && code <= 0x259f) || (code >= 0x2800 && code <= 0x28ff) || (code >= 0xe0b0 && code <= 0xe0b3);
}

export function drawSpecialGlyph(
  ctx: CanvasRenderingContext2D, code: number, x: number, y: number, w: number, h: number, t: number, fg: string, bg: string,
): void {
  ctx.fillStyle = fg;
  if (code >= 0x2580 && code <= 0x259f) return drawBlock(ctx, code, x, y, w, h);
  if (code >= 0x2800) {
    if (code >= 0xe0b0) return drawPowerline(ctx, code, x, y, w, h, t, fg);
    return drawBraille(ctx, code - 0x2800, x, y, w, h);
  }
  const arms = BOX_ARMS[code - 0x2500];
  const cx = x + Math.floor((w - t) / 2);
  const cy = y + Math.floor((h - t) / 2);
  if (arms === "----") return drawOddBox(ctx, code, x, y, w, h, t, cx, cy, fg);
  const L = +arms[0], R = +arms[1], U = +arms[2], D = +arms[3];
  const th = (wt: number) => (wt === 2 ? t * 2 + (t % 2 ? 1 : 0) : wt === 3 ? t * 3 : t);
  const hT = Math.max(th(L), th(R)); // thickness of the horizontal stroke
  const vT = Math.max(th(U), th(D));
  const hy = (wt: number) => cy - Math.floor((th(wt) - t) / 2);
  const vx = (wt: number) => cx - Math.floor((th(wt) - t) / 2);
  const midL = cx - Math.floor((vT - t) / 2); // left edge of the vertical stroke
  const midR = midL + (U || D ? vT : t);
  const midT = cy - Math.floor((hT - t) / 2);
  const midB = midT + (L || R ? hT : t);
  const dbl = (wt: number) => wt === 3;
  // how far each arm reaches into the centre
  const hEnd = (toRight: boolean) => {
    if (dbl(U) || dbl(D)) return toRight ? midL : midR; // forms the outer corner
    if (U || D) return toRight ? midL : midR;
    return toRight ? cx : cx + t;
  };
  const vEnd = (toDown: boolean) => {
    if (dbl(L) || dbl(R)) return toDown ? midT : midB;
    if (L || R) return toDown ? midT : midB;
    return toDown ? cy : cy + t;
  };
  if (L) ctx.fillRect(x, hy(L), hEnd(false) - x, th(L));
  if (R) ctx.fillRect(hEnd(true), hy(R), x + w - hEnd(true), th(R));
  if (U) ctx.fillRect(vx(U), y, th(U), vEnd(false) - y);
  if (D) ctx.fillRect(vx(D), vEnd(true), th(D), y + h - vEnd(true));
  // carve the gap out of double strokes
  if (dbl(L) || dbl(R) || dbl(U) || dbl(D)) {
    ctx.fillStyle = bg;
    const gapH = (toRight: boolean) => {
      if (dbl(U) || dbl(D)) return toRight ? cx : cx + t; // gap joins the perpendicular gap
      if (U || D) return toRight ? midR : midL; // stop at the single line
      return toRight ? cx : cx + t;
    };
    const gapV = (toDown: boolean) => {
      if (dbl(L) || dbl(R)) return toDown ? cy : cy + t;
      if (L || R) return toDown ? midB : midT;
      return toDown ? cy : cy + t;
    };
    if (dbl(L)) ctx.fillRect(x, cy, gapH(false) - x, t);
    if (dbl(R)) ctx.fillRect(gapH(true), cy, x + w - gapH(true), t);
    if (dbl(U)) ctx.fillRect(cx, y, t, gapV(false) - y);
    if (dbl(D)) ctx.fillRect(cx, gapV(true), t, y + h - gapV(true));
    ctx.fillStyle = fg;
  }
}

function drawOddBox(
  ctx: CanvasRenderingContext2D, code: number, x: number, y: number, w: number, h: number, t: number, cx: number, cy: number, fg: string,
): void {
  const dash = (n: number, heavy: boolean, vertical: boolean) => {
    const th = heavy ? t * 2 : t;
    const len = vertical ? h : w;
    const seg = len / n;
    for (let i = 0; i < n; i++) {
      const a = Math.round(i * seg + seg * 0.2);
      const b = Math.round(i * seg + seg * 0.8);
      if (vertical) ctx.fillRect(cx - (heavy ? Math.floor(t / 2) : 0), y + a, th, b - a);
      else ctx.fillRect(x + a, cy - (heavy ? Math.floor(t / 2) : 0), b - a, th);
    }
  };
  switch (code) {
    case 0x2504: return dash(3, false, false);
    case 0x2505: return dash(3, true, false);
    case 0x2506: return dash(3, false, true);
    case 0x2507: return dash(3, true, true);
    case 0x2508: return dash(4, false, false);
    case 0x2509: return dash(4, true, false);
    case 0x250a: return dash(4, false, true);
    case 0x250b: return dash(4, true, true);
    case 0x254c: return dash(2, false, false);
    case 0x254d: return dash(2, true, false);
    case 0x254e: return dash(2, false, true);
    case 0x254f: return dash(2, true, true);
  }
  ctx.strokeStyle = fg;
  ctx.lineWidth = t;
  ctx.beginPath();
  const mx = cx + t / 2;
  const my = cy + t / 2;
  const r = Math.min(w, h) / 2;
  if (code >= 0x256d && code <= 0x2570) {
    // rounded corners
    const right = code === 0x256d || code === 0x2570;
    const down = code === 0x256d || code === 0x256e;
    const ex = right ? x + w : x;
    const ey = down ? y + h : y;
    ctx.moveTo(ex, my);
    ctx.lineTo(mx + (right ? r : -r), my);
    ctx.quadraticCurveTo(mx, my, mx, my + (down ? r : -r));
    ctx.lineTo(mx, ey);
  } else {
    if (code === 0x2571 || code === 0x2573) {
      ctx.moveTo(x + w, y);
      ctx.lineTo(x, y + h);
    }
    if (code === 0x2572 || code === 0x2573) {
      ctx.moveTo(x, y);
      ctx.lineTo(x + w, y + h);
    }
  }
  ctx.stroke();
}

function drawBlock(ctx: CanvasRenderingContext2D, code: number, x: number, y: number, w: number, h: number): void {
  const e = (n: number, of: number, size: number) => Math.round((size * n) / of);
  if (code === 0x2580) return ctx.fillRect(x, y, w, e(1, 2, h));
  if (code >= 0x2581 && code <= 0x2588) {
    const hh = e(code - 0x2580, 8, h);
    return ctx.fillRect(x, y + h - hh, w, hh);
  }
  if (code >= 0x2589 && code <= 0x258f) return ctx.fillRect(x, y, e(0x2590 - code, 8, w), h);
  if (code === 0x2590) return ctx.fillRect(x + e(1, 2, w), y, w - e(1, 2, w), h);
  if (code >= 0x2591 && code <= 0x2593) {
    ctx.globalAlpha = (code - 0x2590) * 0.25;
    ctx.fillRect(x, y, w, h);
    ctx.globalAlpha = 1;
    return;
  }
  if (code === 0x2594) return ctx.fillRect(x, y, w, e(1, 8, h));
  if (code === 0x2595) return ctx.fillRect(x + w - e(1, 8, w), y, e(1, 8, w), h);
  // quadrants: bit 1 upper-left, 2 upper-right, 4 lower-left, 8 lower-right
  const Q = [4, 8, 1, 13, 9, 7, 11, 2, 6, 14][code - 0x2596];
  const hw = e(1, 2, w);
  const hh = e(1, 2, h);
  if (Q & 1) ctx.fillRect(x, y, hw, hh);
  if (Q & 2) ctx.fillRect(x + hw, y, w - hw, hh);
  if (Q & 4) ctx.fillRect(x, y + hh, hw, h - hh);
  if (Q & 8) ctx.fillRect(x + hw, y + hh, w - hw, h - hh);
}

function drawBraille(ctx: CanvasRenderingContext2D, bits: number, x: number, y: number, w: number, h: number): void {
  const dw = Math.max(1, Math.round(w / 5));
  const dh = Math.max(1, Math.round(h / 9));
  const xs = [x + Math.round(w * 0.25 - dw / 2), x + Math.round(w * 0.75 - dw / 2)];
  const ys = [0.125, 0.375, 0.625, 0.875].map((f) => y + Math.round(h * f - dh / 2));
  const map = [[0, 0], [0, 1], [0, 2], [1, 0], [1, 1], [1, 2], [0, 3], [1, 3]];
  for (let i = 0; i < 8; i++) if (bits & (1 << i)) ctx.fillRect(xs[map[i][0]], ys[map[i][1]], dw, dh);
}

function drawPowerline(ctx: CanvasRenderingContext2D, code: number, x: number, y: number, w: number, h: number, t: number, fg: string): void {
  ctx.beginPath();
  const right = code === 0xe0b0 || code === 0xe0b1;
  const a = right ? x : x + w;
  const b = right ? x + w : x;
  ctx.moveTo(a, y);
  ctx.lineTo(b, y + h / 2);
  ctx.lineTo(a, y + h);
  if (code === 0xe0b0 || code === 0xe0b2) {
    ctx.closePath();
    ctx.fill();
  } else {
    ctx.strokeStyle = fg;
    ctx.lineWidth = t;
    ctx.stroke();
  }
}
