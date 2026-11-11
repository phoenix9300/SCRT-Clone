const fs = require('fs'), path = require('path'), os = require('os');
module.exports = async ({ page, shot, cfg, sshd }) => {
  const srv = await sshd();
  fs.mkdirSync(path.join(cfg, 'keys'), { recursive: true });
  fs.copyFileSync(srv.clientKey, path.join(cfg, 'keys', 'client')); fs.chmodSync(path.join(cfg, 'keys', 'client'), 0o600);
  const L = fs.mkdtempSync(path.join(os.tmpdir(), 'sl-')), R = fs.mkdtempSync(path.join(os.tmpdir(), 'sr-'));
  fs.writeFileSync(L + '/a.txt', 'hello a'); fs.mkdirSync(L + '/d'); fs.writeFileSync(L + '/d/b.txt', 'bee');
  fs.writeFileSync(R + '/c.txt', 'remote c'); fs.writeFileSync(R + '/e.txt', 'to delete');
  const until = async (f, what, ms = 8000) => { const t = Date.now(); while (Date.now() - t < ms) { if (f()) return; await page.waitForTimeout(100); } throw new Error('timeout: ' + what); };
  const menu = async (title, item) => { await page.click(`.menu-title:text-is("${title}")`); await page.click(`.popup .item:has-text("${item}")`); };
  const field = (l) => page.locator(`.field:has(label:text-is("${l}")) input`);
  await menu('File', 'Quick Connect');
  await field('Hostname').fill('127.0.0.1'); await field('Port').fill(String(srv.port)); await field('Username').fill(srv.user);
  await page.click('.dialog button.primary');
  await page.click('button:text-is("Accept & Save")', { timeout: 10000 });
  await page.waitForSelector('.tab .state.connected', { timeout: 10000 });
  await menu('File', 'Connect SFTP Tab');
  await page.waitForSelector('.sftp .sftp-pane[data-pane=remote] tr[data-i]', { timeout: 10000 });
  const go = async (pane, p) => { const i = page.locator(`[data-pane=${pane}] header input`); await i.fill(p); await i.press('Enter'); };
  const row = (pane, n) => page.locator(`.sftp [data-pane=${pane}] tr[data-name="${n}"]`);
  const ctx = async (pane, n, item) => { await row(pane, n).click(); await row(pane, n).click({ button: 'right' }); await page.click(`.popup .item:has-text("${item}")`); };
  await go('local', L); await go('remote', R);
  await row('local', 'a.txt').waitFor(); await row('remote', 'c.txt').waitFor();
  // upload file + dir (multi-select)
  await row('local', 'a.txt').click(); await row('local', 'd').click({ modifiers: ['Control'] });
  await row('local', 'd').click({ button: 'right' }); await page.click('.popup .item:has-text("Upload")');
  await until(() => fs.existsSync(R + '/a.txt') && fs.existsSync(R + '/d/b.txt'), 'upload');
  if (fs.readFileSync(R + '/d/b.txt', 'utf8') !== 'bee') throw new Error('dir content');
  await row('remote', 'd').waitFor(); // pane refreshed after job
  // download via double-click
  await row('remote', 'c.txt').dblclick();
  await page.waitForSelector('.sftp-job.done:has-text("c.txt")'); await until(() => fs.existsSync(L + '/c.txt'), 'download');
  if (fs.readFileSync(L + '/c.txt', 'utf8') !== 'remote c') throw new Error('download content');
  await page.waitForSelector('.sftp-job.done');
  // rename
  await row('remote', 'a.txt').click(); await page.keyboard.press('F2');
  await page.fill('.dialog input', 'renamed.txt'); await page.keyboard.press('Enter');
  await until(() => fs.existsSync(R + '/renamed.txt') && !fs.existsSync(R + '/a.txt'), 'rename');
  // chmod
  await ctx('remote', 'renamed.txt', 'Permissions');
  await page.fill('.dialog input.mono', '600'); await page.click('.dialog button:text-is("Apply")');
  await until(() => (fs.statSync(R + '/renamed.txt').mode & 0o777) === 0o600, 'chmod');
  await row('remote', 'renamed.txt').locator('td').nth(3).waitFor();
  // edit
  await ctx('remote', 'c.txt', 'View / Edit');
  await page.fill('.dialog textarea', 'edited!'); await page.click('.dialog button:text-is("Save")');
  await until(() => fs.readFileSync(R + '/c.txt', 'utf8') === 'edited!', 'edit');
  // mkdir
  await page.click('[data-pane=remote] button[title="New folder"]');
  await page.fill('.dialog input', 'nf'); await page.keyboard.press('Enter');
  await until(() => fs.existsSync(R + '/nf'), 'mkdir');
  // delete
  await row('remote', 'e.txt').click(); await page.keyboard.press('Delete');
  await page.click('.dialog button.danger');
  await until(() => !fs.existsSync(R + '/e.txt'), 'delete');
  await shot('sftp');
  // clear finished
  await page.click('button:text-is("Clear Finished")');
  await until(() => true, ''); await page.waitForTimeout(300);
  if (await page.locator('.sftp-job').count()) throw new Error('jobs not cleared');
  const ov = await page.evaluate(() => document.querySelector('.sftp').scrollWidth > document.querySelector('.sftp').clientWidth);
  if (ov) throw new Error('horizontal overflow');
};
