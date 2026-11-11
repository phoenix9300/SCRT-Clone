// Produces the screenshots used in the README (E2E_OUT=docs/screenshots).
const fs = require('fs'), path = require('path');
module.exports = async ({ page, shot, waitText, cfg, sshd }) => {
  const srv = await sshd();
  fs.mkdirSync(path.join(cfg, 'keys'), { recursive: true });
  fs.copyFileSync(srv.clientKey, path.join(cfg, 'keys', 'id_ed25519'));
  const rpcSave = (p, ov) => page.evaluate(([p, ov]) => window.__hawser.rpc('sessions.save', { path: p, overrides: ov }), [p, ov]);
  await rpcSave('Lab/core-sw1', { host: '10.20.0.1', username: 'netops', terminal: { emulation: 'vt100' } });
  await rpcSave('Lab/core-sw2', { host: '10.20.0.2', username: 'netops' });
  await rpcSave('Lab/console-a', { protocol: 'serial', serial: { port: '/dev/ttyUSB0', baud: 115200 } });
  await rpcSave('Production/web-01', { host: '127.0.0.1', port: srv.port, username: srv.user, terminal: { keywordSet: 'Network' } });
  await rpcSave('Production/legacy-pbx', { protocol: 'telnet', host: 'pbx.example.net' });
  await rpcSave('Local shell', { protocol: 'local' });
  await page.waitForSelector('.tree-row:has-text("web-01")');
  await page.dblclick('.tree-row:has-text("web-01")');
  await page.click('button:text-is("Accept & Save")', { timeout: 10000 });
  await waitText(/[$#] ?$/m, 15000);
  const ps1 = String.raw`PS1='\[\e[32m\]ops@gateway\[\e[0m\]:\w\$ '; clear`;
  await page.keyboard.type(ps1 + '; cd /usr/share; ls --color -la | head -8; echo "link up, 2 errors, status ok on 192.168.10.4"\n');
  await waitText(/status ok/);
  await page.dblclick('.tree-row:has-text("Local shell")');
  // wait for the new tab itself, not the prompt of the previous one
  await page.waitForFunction(() => document.querySelectorAll('.tab').length === 2);
  await page.waitForFunction(() => { const s = window.__hawser.screen().join('\n'); return !/status ok/.test(s) && /[$#] ?$/m.test(s); }, null, { timeout: 15000 });
  await page.keyboard.type(ps1 + '; cd /usr/lib; ls --color -la | head -14\n');
  await page.keyboard.type(String.raw`for i in $(seq 16 87); do printf '\e[48;5;%dm  ' $i; done; printf '\e[0m\n\e[1mbold\e[0m \e[3mitalic\e[0m \e[4munderline\e[0m \e[7minverse\e[0m \e[38;2;255;140;0mtruecolor\e[0m\n'` + '\n');
  await page.keyboard.type('echo "interface Gi0/1 is up, line protocol is down (error: timeout) at 10.0.0.1"; echo done-shot\n');
  await waitText(/^done-shot/m);
  await shot('main');
  await page.locator('.tab').first().click();
  await page.click('.menu-title:text-is("Options")');
  await page.click('.popup .item:has-text("Session Options")');
  await page.waitForSelector('.dialog');
  await page.waitForTimeout(300);
  await shot('session-options');
  await page.keyboard.press('Escape');
  await page.click('.menu-title:text-is("File")');
  await page.click('.popup .item:has-text("Connect SFTP Tab")');
  await page.waitForSelector('.sftp tr[data-i]', { timeout: 10000 });
  // neutral directories for the picture
  for (const [pane, dir] of [['local', '/usr/share/doc'], ['remote', '/etc']]) {
    const i = page.locator(`[data-pane=${pane}] header input`);
    await i.fill(dir);
    await i.press('Enter');
  }
  await page.waitForSelector('[data-pane=remote] tr[data-name="hosts"]', { timeout: 10000 });
  await page.waitForTimeout(400);
  await shot('sftp');
};
