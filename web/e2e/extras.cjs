// Shared credentials, the raw-data (hex) view, sessions opened at startup and
// Kermit transfers through the menus.
const fs = require('fs'), path = require('path'), os = require('os'), crypto = require('crypto');
module.exports = async ({ page, waitText, cfg, sshd }) => {
  const h = require('./lib.cjs')(page);
  const { must, until, menu } = h;
  const rpc = (m, p) => page.evaluate(([m, p]) => window.__hawser.rpc(m, p), [m, p]);

  // --- credentials manager
  await menu('Tools', 'Credentials…');
  const dlg = h.dialog('Credentials');
  await dlg.locator('button:has-text("New")').click();
  await h.field('Name').fill('lab-admin');
  await h.field('Username').fill('netops');
  await h.field('Password').fill('s3cret-pw');
  await dlg.locator('button.primary:has-text("Save")').click();
  await dlg.locator('.nav-item:text-is("lab-admin")').waitFor();
  const list = await rpc('credentials.list');
  must(list.length === 1 && list[0].username === 'netops' && list[0].hasPassword && !JSON.stringify(list).includes('s3cret-pw'), 'credentials.list ' + JSON.stringify(list));
  must(!fs.readFileSync(path.join(cfg, 'vault.json'), 'utf8').includes('s3cret-pw'), 'password stored in clear text');
  await dlg.locator('footer button:has-text("Close")').click();
  // a session can pick it on its Connection page, and the choice is saved
  await rpc('sessions.save', { path: 'router', overrides: { host: '192.0.2.1' }, secrets: {} });
  await page.locator('.tree-row:has(.name:text-is("router"))').click({ button: 'right' });
  await page.click('.popup .item:has(.label:text-is("Properties…"))');
  const sel = page.locator('.dialog').last().locator('.field:has(label:text-is("Credential")) select');
  await sel.selectOption('lab-admin');
  await h.okDialog();
  await until(async () => (await rpc('sessions.get', { path: 'router' })).overrides.credential === 'lab-admin', 'credential saved in session');

  // --- open at startup
  await rpc('sessions.save', { path: 'auto shell', overrides: { protocol: 'local' }, secrets: {} });
  await page.locator('.tree-row:has(.name:text-is("auto shell"))').click({ button: 'right' });
  await page.click('.popup .item:has(.label:text-is("Open at Startup"))');
  await until(async () => ((await rpc('global.get')).autoSessions || []).includes('auto shell'), 'autoSessions saved');
  must((await h.tabs()) === 0, 'no tabs expected yet');
  await page.reload();
  await page.waitForSelector('.menubar');
  await until(async () => (await h.tabs()) === 1, 'startup session opened');
  await waitText(/[$#] ?$/m, 15000);

  // --- hex view shows what was sent and received
  await page.keyboard.type('echo hexview-$((3+4))\n');
  await waitText(/^hexview-7/m);
  await menu('View', 'Raw Data (Hex View)…');
  const hv = h.dialog('Raw Data (Hex View)');
  const both = await hv.locator('pre').innerText();
  must(/── sent ──/.test(both) && /── received ──/.test(both), 'hex view directions missing');
  // typing and its echo interleave, so look at one direction at a time
  await hv.locator('label.check:has-text("Received")').click();
  const sent = await hv.locator('pre').innerText();
  must(sent.includes('65 63 68 6f 20 68 65 78') && !/received/.test(sent), 'sent bytes of "echo hex" not shown: ' + sent.slice(0, 200));
  await hv.locator('label.check:has-text("Received")').click();
  await hv.locator('label.check:has-text("Sent")').click();
  const recv = await hv.locator('pre').innerText();
  must(/hexview-7/.test(recv.split('\n').map((l) => l.slice(58)).join('')), 'received text not shown');
  await page.keyboard.press('Escape');

  // --- Kermit through the Transfer menu over SSH
  if (!fs.existsSync('/usr/bin/gkermit')) { console.log('gkermit not installed: Kermit part skipped'); return; }
  const srv = await sshd();
  fs.mkdirSync(path.join(cfg, 'keys'), { recursive: true });
  fs.copyFileSync(srv.clientKey, path.join(cfg, 'keys', 'client')); fs.chmodSync(path.join(cfg, 'keys', 'client'), 0o600);
  const tmp = (p) => fs.mkdtempSync(path.join(os.tmpdir(), p));
  const src = tmp('ks-'), down = tmp('kd-'), up = tmp('ku-');
  const data = crypto.randomBytes(60000);
  fs.writeFileSync(path.join(src, 'kdata.bin'), data);
  await rpc('sessions.save', { path: 'kermit', overrides: { protocol: 'ssh2', host: '127.0.0.1', port: srv.port, username: srv.user, transfer: { downloadDir: down } }, secrets: {} });
  await page.locator('.tree-row:has(.name:text-is("kermit"))').dblclick();
  await page.click('button:text-is("Accept & Save")', { timeout: 10000 });
  await until(async () => (await h.tabs()) === 2, 'ssh tab');
  await page.waitForFunction(() => { const s = window.__hawser.screen().join('\n'); return !/hexview/.test(s) && /[$#] ?$/m.test(s); }, null, { timeout: 15000 });
  await page.keyboard.type(`cd ${src} && gkermit -i -s kdata.bin; echo ksend-$((1+1))\n`);
  await waitText(/KERMIT READY TO SEND/);
  await menu('Transfer', 'Kermit Download');
  await waitText(/^ksend-2/m, 30000);
  await until(() => fs.existsSync(path.join(down, 'kdata.bin')) && fs.readFileSync(path.join(down, 'kdata.bin')).equals(data), 'kermit download content');
  await h.focusTerm();
  await page.keyboard.type(`cd ${up} && gkermit -i -r; echo krecv-$((1+1))\n`);
  await waitText(/KERMIT READY TO RECEIVE|gkermit -i -r/);
  await page.waitForTimeout(400);
  await menu('Transfer', 'Kermit Upload…');
  const fp = page.locator('.dialog').last();
  const pathBox = fp.locator('input[aria-label="Path"]');
  await pathBox.fill(src); await pathBox.press('Enter');
  await fp.locator('tr:has-text("kdata.bin")').click();
  await fp.locator('footer button.primary').click();
  await waitText(/^krecv-2/m, 30000);
  await until(() => fs.existsSync(path.join(up, 'kdata.bin')) && fs.readFileSync(path.join(up, 'kdata.bin')).equals(data), 'kermit upload content');
};
