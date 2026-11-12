// Session logging and in-band transfers (Zmodem up/down, auto prompt, Send
// ASCII) driven through the UI against a private OpenSSH server.
const fs = require('fs'), path = require('path'), os = require('os'), crypto = require('crypto');
module.exports = async ({ page, waitText, cfg, sshd }) => {
  const h = require('./lib.cjs')(page);
  const { must, until, menu } = h;
  const srv = await sshd();
  fs.mkdirSync(path.join(cfg, 'keys'), { recursive: true });
  fs.copyFileSync(srv.clientKey, path.join(cfg, 'keys', 'client')); fs.chmodSync(path.join(cfg, 'keys', 'client'), 0o600);
  const tmp = (p) => fs.mkdtempSync(path.join(os.tmpdir(), p));
  const L = tmp('xl-'), R = tmp('xr-'), R2 = tmp('xr2-'), D = tmp('xd-');
  const type = (s) => page.keyboard.type(s + '\n');
  const read = (p) => (fs.existsSync(p) ? fs.readFileSync(p) : null);
  const base = { protocol: 'ssh2', host: '127.0.0.1', port: srv.port, username: srv.user };
  await page.evaluate(([b, D]) => Promise.all([
    window.__hawser.rpc('sessions.save', { path: 'zauto', overrides: { ...b, transfer: { downloadDir: D, zmodemAuto: true } }, secrets: {} }),
    window.__hawser.rpc('sessions.save', { path: 'zmanual', overrides: { ...b, transfer: { downloadDir: D, zmodemAuto: false } }, secrets: {} }),
  ]), [base, D]);
  const open = async (name, n) => {
    await page.locator(`.tree-row:has(.name:text-is("${name}"))`).dblclick();
    await until(async () => (await h.tabs()) === n, 'tab ' + name);
  };
  const picker = async (dir, file, title) => {
    const d = page.locator(`.dialog[aria-label="${title}"]`);
    await d.waitFor({ timeout: 10000 });
    await h.goDir(d, dir);
    await d.locator(`tr:has-text("${file}")`).click();
    await d.locator('footer button.primary').click();
    await d.waitFor({ state: 'detached' });
  };
  const completeToast = () => page.locator('.toast:has-text("Transfer complete")').first().waitFor({ timeout: 15000 });

  // ---- connect (first time: accept the host key)
  await open('zauto', 1);
  await page.click('button:text-is("Accept & Save")', { timeout: 10000 });
  await page.waitForSelector('.tab .state.connected', { timeout: 10000 });
  await waitText(/[$#] ?$/m);

  // ---- logging: clean text, toggled on and off
  await menu('File', 'Log Session');
  const t = await page.locator('.toast:has-text("Logging to")').first().innerText();
  const logFile = /Logging to (.+)/.exec(t)[1].trim();
  await page.locator('.statusbar .cell:text-is("LOG")').waitFor();
  must((await page.locator('.statusbar .cell:text-is("LOG")').getAttribute('title')) === logFile, 'status bar tooltip shows the log path');
  await type(`printf '\\033[1;31mred\\033[0m-log%s\\n' $((20+1))`); await waitText(/^red-log21/m);
  await until(() => /red-log21/.test(String(read(logFile))), 'log content');
  await menu('File', 'Log Session');
  await until(async () => (await page.locator('.statusbar .cell:text-is("LOG")').count()) === 0, 'LOG indicator off');
  await type('echo after-log$((1+1))'); await waitText(/^after-log2/m);
  await page.waitForTimeout(300);
  const log = String(read(logFile));
  must(!/\x1b/.test(log), 'log must not contain escape sequences: ' + JSON.stringify(log));
  must(!/after-log2/.test(log), 'nothing logged after stopping');
  must(/echo|printf/.test(log) || /red-log21/.test(log), 'log content sanity');

  // ---- zmodem: rz started by hand triggers the upload prompt
  fs.writeFileSync(path.join(L, 'up.txt'), 'upload-data-123\n');
  fs.writeFileSync(path.join(L, 'ascii.txt'), 'line one\nline two\n');
  const bin = crypto.randomBytes(20000); fs.writeFileSync(path.join(R, 'down.bin'), bin);
  await type(`cd ${R}; rz`);
  await picker(L, 'up.txt', 'ZMODEM: files to send');
  await until(() => String(read(path.join(R, 'up.txt'))) === 'upload-data-123\n', 'auto-prompt upload arrived');
  await completeToast();
  await page.locator('.toast:has-text("Transfer complete")').first().click(); // dismiss
  await h.focusTerm();
  await waitText(/[$#] ?$/m);
  // ---- sz downloads into the session's download folder
  await type(`sz ${R}/down.bin`);
  await until(() => { const b = read(path.join(D, 'down.bin')); return b && b.equals(bin); }, 'download into the download folder ' + fs.readdirSync(D) + (await page.evaluate(() => window.__hawser.screen().join('|') + document.querySelector('.toasts').innerText)));
  await completeToast();
  await page.locator('.toast:has-text("Transfer complete")').first().click();
  await h.focusTerm();
  await waitText(/[$#] ?$/m);
  // ---- Send ASCII into cat
  await type(`cat > ${R}/out.txt`);
  await menu('Transfer', 'Send ASCII…');
  await picker(L, 'ascii.txt', 'ASCII upload');
  await until(() => String(read(path.join(R, 'out.txt'))) === 'line one\nline two\n', 'ASCII text typed into cat');
  await h.focusTerm();
  await page.keyboard.press('Control+d');
  await waitText(/[$#] ?$/m);

  // ---- manual Zmodem Upload… (automatic detection off)
  fs.writeFileSync(path.join(L, 'up2.txt'), 'manual-upload');
  await open('zmanual', 2);
  await page.waitForSelector('.tab:nth-child(2) .state.connected', { timeout: 10000 });
  await waitText(/[$#] ?$/m);
  await type(`cd ${R2}; rz`);
  await waitText(/waiting to receive/);
  await menu('Transfer', 'Zmodem Upload…');
  await picker(L, 'up2.txt', 'ZMODEM upload');
  await until(() => String(read(path.join(R2, 'up2.txt'))) === 'manual-upload', 'manual upload arrived');
  await completeToast();
};
