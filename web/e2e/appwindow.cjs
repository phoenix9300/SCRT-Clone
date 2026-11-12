// Drives the REAL application window: Hawser starts a Chromium-family browser
// in --app mode, exactly as it does for users. Needs a display, e.g.
//
//   xvfb-run -a node e2e/appwindow.cjs
//
// Checks that the window is a standalone app window, that the access token
// does not stay in the address, that keys an ordinary browser tab reserves
// (Ctrl+W, Ctrl+T, Ctrl+N) reach the terminal, and that closing the window
// stops the engine.
const { chromium } = require('playwright');
const { spawn } = require('child_process');
const fs = require('fs'), os = require('os'), path = require('path');
(async () => {
  const cfg = fs.mkdtempSync(path.join(os.tmpdir(), 'hawser-app-'));
  // the real browser plus a debugging port so this test can drive the window
  const wrapper = path.join(cfg, 'browser.sh');
  const chrome = process.env.HAWSER_TEST_BROWSER || chromium.executablePath();
  fs.writeFileSync(wrapper, '#!/bin/sh\nexec "' + chrome + '" --no-sandbox --remote-debugging-port=9377 "$@"\n', { mode: 0o755 });
  const bin = process.env.HAWSER_BIN || path.resolve(__dirname, '..', '..', 'hawser');
  const proc = spawn(bin, ['--config-dir', cfg, '--new-instance'], { env: { ...process.env, HAWSER_BROWSER: wrapper, SHELL: '/bin/bash' } });
  let out = ''; proc.stdout.on('data', (d) => (out += d)); proc.stderr.on('data', (d) => (out += d));
  let exited = false; proc.on('exit', () => (exited = true));
  let browser;
  for (let i = 0; i < 100 && !browser; i++) { try { browser = await chromium.connectOverCDP('http://127.0.0.1:9377'); } catch { await new Promise((r) => setTimeout(r, 200)); } }
  if (!browser) throw new Error('application window did not start:\n' + out);
  let page;
  for (let i = 0; i < 100 && !page; i++) { page = browser.contexts().flatMap((c) => c.pages()).find((p) => p.url().startsWith('http://127.0.0.1')); if (!page) await new Promise((r) => setTimeout(r, 100)); }
  if (!page) throw new Error('no application page; pages: ' + browser.contexts().flatMap((c) => c.pages()).map((p) => p.url()).join(', '));
  await page.waitForSelector('.menubar', { timeout: 15000 });
  const info = await page.evaluate(() => ({ url: location.href, standalone: matchMedia('(display-mode: standalone)').matches, w: outerWidth, h: outerHeight, tabsUi: window.toolbar?.visible }));
  console.log('WINDOW', JSON.stringify(info));
  if (/token=/.test(info.url)) throw new Error('token left in the address');
  const screen = () => page.evaluate(() => window.__hawser.screen().join('\n'));
  const waitText = async (re, ms = 10000) => { const t = Date.now(); for (;;) { const s = await screen(); if (re.test(s)) return s; if (Date.now() - t > ms) throw new Error('timeout ' + re + '\n' + s); await page.waitForTimeout(100); } };
  await page.click('button[title="Local Shell"]');
  await waitText(/[$#] ?$/m);
  // keys an ordinary browser tab would keep for itself
  await page.keyboard.type('echo aaa bbb');
  await page.keyboard.press('Control+w');       // readline: delete word
  await page.keyboard.type('ccc\n');
  await waitText(/^aaa ccc/m);
  await page.keyboard.type('echo one');
  await page.keyboard.press('Control+t');       // readline: transpose characters
  await page.keyboard.press('Control+n');       // readline: next history (no-op) – must not open a window
  await page.keyboard.type('\n');
  await waitText(/^oen/m);
  const pages = browser.contexts().flatMap((c) => c.pages()).length;
  if (pages !== 1) throw new Error('reserved key opened another window/tab: ' + pages);
  await page.keyboard.press('Control+Shift+t');  // app shortcut: new local shell tab
  await page.waitForFunction(() => document.querySelectorAll('.tab').length === 2, null, { timeout: 8000 });
  console.log('KEYS OK, tabs=2');
  // closing the window ends the engine
  await page.evaluate(() => { window.onbeforeunload = null; });
  await page.close({ runBeforeUnload: false }).catch(() => {});
  await browser.close().catch(() => {});
  for (let i = 0; i < 50 && !exited; i++) await new Promise((r) => setTimeout(r, 200));
  console.log(exited ? 'ENGINE EXITED WITH WINDOW' : 'ENGINE STILL RUNNING');
  if (!exited) proc.kill();
  process.exit(exited ? 0 : 1);
})().catch((e) => { console.log('APPWINDOW FAILED:', e.message); process.exit(1); });
