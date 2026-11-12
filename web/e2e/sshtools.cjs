// Tools that need a live SSH session: port forwarding, connection details,
// key conversion, clone, reconnect (button and Enter), disconnect.
const fs = require('fs'), path = require('path'), os = require('os'), net = require('net');
const { execFileSync } = require('child_process');
module.exports = async ({ page, waitText, cfg, sshd }) => {
  const h = require('./lib.cjs')(page);
  const { must, until, menu } = h;
  const srv = await sshd();
  fs.mkdirSync(path.join(cfg, 'keys'), { recursive: true });
  fs.copyFileSync(srv.clientKey, path.join(cfg, 'keys', 'client')); fs.chmodSync(path.join(cfg, 'keys', 'client'), 0o600);
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'sshtools-'));
  const type = (s) => page.keyboard.type(s + '\n');
  const connected = () => page.locator('.group.focus .tab.active .state.connected, .group.focus .tab.active .state.activity').count();

  // ---- connect through Quick Connect
  await menu('File', 'Quick Connect…');
  await h.field('Hostname').fill('127.0.0.1'); await h.field('Port').fill(String(srv.port)); await h.field('Username').fill(srv.user);
  await h.okDialog();
  await page.click('button:text-is("Accept & Save")', { timeout: 10000 });
  await until(async () => (await connected()) === 1, 'connected');
  await waitText(/[$#] ?$/m);

  // ---- connection details
  await menu('Tools', 'Connection Details…');
  const det = page.locator('.dialog[aria-label="Connection Details"]');
  await det.waitFor();
  await until(async () => /Cipher/i.test(await det.innerText()), 'details loaded');
  const dt = await det.innerText();
  must(/Key exchange/i.test(dt), 'kex missing: ' + dt);
  must(/Server version[\s\S]*OpenSSH/i.test(dt), 'server version missing: ' + dt);
  must(/Cipher\s*\n?\s*[a-z0-9@.-]+/i.test(dt) && /(chacha|aes)/i.test(dt), 'cipher value missing: ' + dt);
  await det.locator('footer button').click();

  // ---- port forwarding to an echo server
  const echo = net.createServer((s) => s.on('data', (d) => s.write(d)));
  await new Promise((r) => echo.listen(0, '127.0.0.1', r));
  const echoPort = echo.address().port;
  const bindPort = 30000 + Math.floor(Math.random() * 20000);
  await menu('Tools', 'Port Forwarding…');
  const pf = page.locator('.dialog[aria-label="Port Forwarding"]');
  await pf.waitFor();
  await pf.locator('input[aria-label="Name"]').fill('fw1');
  await pf.locator('input[aria-label="Bind address"]').fill('127.0.0.1');
  await pf.locator('.tl-form-row').first().locator('input[type=number]').fill(String(bindPort));
  await pf.locator('input[aria-label="Destination host"]').fill('127.0.0.1');
  await pf.locator('.tl-form-row').nth(1).locator('input[type=number]').fill(String(echoPort));
  await pf.locator('button.primary:text-is("Add")').click();
  const frow = pf.locator('table[aria-label="Port forwards"] tr:has-text("fw1")');
  await frow.waitFor();
  await until(async () => /listening/.test(await frow.innerText()), 'forward listening');
  const addr = (await frow.locator('td').nth(2).innerText()).trim();
  must(addr.includes(String(bindPort)), 'listen address ' + addr);
  const reply = await new Promise((resolve, reject) => {
    const c = net.connect(bindPort, '127.0.0.1', () => c.write('ping-through-tunnel'));
    const t = setTimeout(() => reject(new Error('echo timeout')), 5000);
    c.on('data', (d) => { clearTimeout(t); c.end(); resolve(String(d)); });
    c.on('error', reject);
  });
  must(reply === 'ping-through-tunnel', 'echo reply ' + reply);
  await until(async () => (await frow.locator('td').nth(6).innerText()).trim() === '1', 'total counter shows 1 connection', 8000);
  await frow.click();
  await pf.locator('button:text-is("Remove")').click();
  await pf.locator('td:has-text("No port forwards")').waitFor();
  await new Promise((r) => net.connect(bindPort, '127.0.0.1').on('error', r).on('connect', () => r('still listening')))
    .then((v) => must(v !== 'still listening', 'forward must be closed after removal'));
  await pf.locator('footer button').click();
  echo.close();

  // ---- convert the generated key to PPK with a passphrase
  const ppk = path.join(tmp, 'client.ppk'), pwfile = path.join(tmp, 'pw');
  fs.writeFileSync(pwfile, 'correct horse');
  await menu('Tools', 'Convert Private Key…');
  const cv = page.locator('.dialog[aria-label="Convert Private Key"]');
  await cv.waitFor();
  await cv.locator('button:text-is("Browse…")').first().click();
  const pk = page.locator('.dialog[aria-label="Choose a private key"]');
  await h.goDir(pk, path.dirname(srv.clientKey));
  await pk.locator('tr:has(span.row:text-is("client"))').click();
  await pk.locator('footer button.primary').click();
  await cv.locator('.field:has(label:text-is("Target format")) select').selectOption('ppk');
  await cv.locator('.field:has(label:text-is("New passphrase")) input').fill('correct horse');
  await cv.locator('.field:has(label:text-is("Confirm passphrase")) input').fill('correct horse');
  await cv.locator('.field:has(label:text-is("Output file")) input').fill(ppk);
  await cv.locator('footer button.primary').click();
  await page.locator('.toast:has-text("Converted key written")').waitFor();
  const want = fs.readFileSync(srv.clientKey + '.pub', 'utf8').trim().split(/\s+/).slice(0, 2).join(' ');
  const got = execFileSync('puttygen', [ppk, '-O', 'public-openssh', '--old-passphrase', pwfile]).toString().trim().split(/\s+/).slice(0, 2).join(' ');
  must(want === got, `PPK public key differs:\n${want}\n${got}`);
  must(/Encryption: aes256-cbc/.test(fs.readFileSync(ppk, 'utf8')), 'PPK should be encrypted');

  // ---- clone: same connection, no new host key prompt
  await h.focusTerm();
  await menu('File', 'Clone Session');
  await until(async () => (await h.tabs()) === 2, 'clone tab');
  await until(async () => (await connected()) === 1, 'clone connected');
  must((await page.locator('.dialog').count()) === 0, 'clone must not prompt for the host key');
  await waitText(/[$#] ?$/m);

  // ---- exit -> reconnect via overlay button, then via Enter
  await h.focusTerm();
  await type('exit');
  const overlay = page.locator('.group.focus .view.active .overlay-msg');
  await overlay.waitFor();
  await overlay.locator('button:text-is("Reconnect")').click();
  await until(async () => (await connected()) === 1 && (await overlay.count()) === 0, 'reconnected by button');
  await waitText(/[$#] ?$/m);
  await h.focusTerm();
  await type('echo r1-$((1+1))'); await waitText(/^r1-2/m);
  await type('exit');
  await overlay.waitFor();
  await h.focusTerm();
  await page.keyboard.press('Enter');
  await until(async () => (await connected()) === 1 && (await overlay.count()) === 0, 'reconnected by Enter');
  await waitText(/[$#] ?$/m);
  await h.focusTerm();
  await type('echo r2-$((2+2))'); await waitText(/^r2-4/m);

  // ---- disconnect from the menu
  await menu('File', 'Disconnect');
  await overlay.waitFor();
  await until(async () => (await connected()) === 0, 'tab shows disconnected');
  await menu('File', 'Reconnect');
  await until(async () => (await connected()) === 1, 'reconnect from the menu');
};
