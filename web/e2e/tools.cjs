const fs = require('fs'), os = require('os'), path = require('path'), dgram = require('dgram');
const { execFileSync } = require('child_process');
module.exports = async ({ page, shot, cfg }) => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'tools-e2e-'));
  const menu = async (item, title = 'Tools') => {
    await page.click(`.menu-title:has-text("${title}")`);
    await page.click(`.popup .item:has-text("${item}")`);
  };
  const dlg = page.locator('.dialog').last();
  const field = (label) => page.locator('.dialog').last().locator('.field', { hasText: label });
  const must = (c, m) => { if (!c) throw new Error(m); };
  // --- keygen
  await menu('Create Public Key');
  await field('Key type').locator('select').selectOption('ed25519-0');
  await field('Comment').locator('input').fill('e2e@test');
  await dlg.locator('button.primary', { hasText: 'Generate' }).click();
  await page.waitForSelector('text=Key created');
  const text = await dlg.innerText();
  const m = /SHA-256\s*\n?\s*(SHA256:\S+)/.exec(text);
  must(m, 'no sha256 shown: ' + text);
  const keyPath = path.join(cfg, 'keys', 'id_ed25519');
  must(fs.existsSync(keyPath) && fs.existsSync(keyPath + '.pub'), 'key files missing');
  const kg = execFileSync('ssh-keygen', ['-l', '-f', keyPath + '.pub']).toString();
  must(kg.includes(m[1]), `fingerprint mismatch: ${kg} vs ${m[1]}`);
  must((await dlg.locator('pre.tl-art').innerText()).includes('+'), 'no random art');
  await dlg.locator('button', { hasText: 'Add to Agent' }).click();
  await page.waitForSelector('text=Key added to the agent');
  await dlg.locator('button.primary', { hasText: 'Close' }).click();
  // --- agent list
  await menu('Manage Agent Keys');
  await page.waitForSelector('table[aria-label="Agent keys"] td:has-text("e2e@test")');
  console.log('keygen + agent OK', m[1]);
  await page.keyboard.press('Escape');
  // --- tftp
  const root = path.join(tmp, 'tftp'); fs.mkdirSync(root);
  fs.writeFileSync(path.join(root, 'hello.txt'), 'hello tftp');
  await menu('TFTP Server', 'Transfer');
  await field('Root directory').locator('input').fill(root);
  await field('Port').locator('input').fill('46969');
  await dlg.locator('button.primary', { hasText: 'Start' }).click();
  await page.waitForSelector('text=Listening on');
  const got = await new Promise((resolve, reject) => {
    const s = dgram.createSocket('udp4'); let buf = '';
    const t = setTimeout(() => reject(new Error('tftp timeout')), 5000);
    s.on('message', (msg, r) => {
      if (msg.readUInt16BE(0) === 3) { buf += msg.slice(4).toString(); const ack = Buffer.alloc(4); ack.writeUInt16BE(4, 0); ack.writeUInt16BE(msg.readUInt16BE(2), 2); const last = msg.length < 516; s.send(ack, r.port, r.address, () => { if (last) { clearTimeout(t); s.close(); resolve(buf); } }); }
      else { reject(new Error('tftp error ' + msg.slice(4))); }
    });
    s.send(Buffer.concat([Buffer.from([0, 1]), Buffer.from('hello.txt\0octet\0')]), 46969, '127.0.0.1');
  });
  must(got === 'hello tftp', 'tftp payload ' + got);
  await page.waitForTimeout(800); console.log('LOG', JSON.stringify(await page.locator('[data-testid=tftp-log]').innerText()));
  await page.waitForFunction(() => /done\s.*hello\.txt/.test(document.querySelector('[data-testid=tftp-log]').textContent));
  console.log('tftp OK:', (await page.locator('[data-testid=tftp-log]').innerText()).trim().split('\n').pop());
  await dlg.locator('button', { hasText: 'Stop' }).click();
  await page.waitForSelector('text=Stopped');
  await dlg.locator('button', { hasText: 'Close' }).click();
  // --- import
  const conf = path.join(tmp, 'ssh_config');
  fs.writeFileSync(conf, 'Host *\n ServerAliveInterval 9\nHost jump\n HostName 10.1.1.1\n User ops\nHost web\n HostName 10.1.1.2\n User admin\n Port 2200\n ProxyJump jump\n LocalForward 8080 localhost:80\n');
  await menu('Import Sessions');
  await field('File').locator('input').fill(conf);
  await dlg.locator('button.primary', { hasText: 'Preview' }).click();
  await page.waitForSelector('table[aria-label="Sessions found"] td:has-text("web")');
  await field('Target folder').locator('input').fill('Imported');
  await dlg.locator('button.primary', { hasText: 'Import 2' }).click();
  await page.waitForSelector('text=Import finished');
  await dlg.locator('button', { hasText: 'Close' }).click();
  const web = JSON.parse(fs.readFileSync(path.join(cfg, 'sessions', 'Imported', 'web.json'), 'utf8'));
  must(web.host === '10.1.1.2' && web.port === 2200 && web.firewall === 'session:jump', 'bad session ' + JSON.stringify(web));
  await page.waitForSelector('.tree-row:has-text("Imported")');
  // --- export
  const out = path.join(tmp, 'export.json');
  await menu('Export Sessions');
  // a name containing a path separator is taken as the full destination
  await page.locator('.dialog').last().locator('input:not([aria-label="Path"])').last().fill(out);
  await page.locator('.dialog').last().locator('button.primary').click();
  await page.waitForSelector('text=Exported 2 sessions');
  const ex = JSON.parse(fs.readFileSync(out, 'utf8'));
  must(ex.version === 1 && ex.sessions.length === 2 && ex.sessions.some((s) => s.path === 'Imported/web'), 'bad export ' + JSON.stringify(ex));
  console.log('import/export OK');
  // --- other dialogs open
  for (const [item, title] of [['Host Key Database', 'Host Key Database'], ['Credential Vault', 'Credential Vault'], ['Convert Private Key', 'Convert Private Key']]) {
    await menu(item); await page.waitForSelector(`.dialog[aria-label="${title}"]`); await page.keyboard.press('Escape');
  }
  await menu('Credential Vault');
  await page.waitForSelector('text=Unlocked');
  await shot('tools-vault');
};
