// Session manager: folders, sessions, filter, rename/move, duplicate, delete,
// connect (double click and Connect All), context menus, Quick Connect + save.
const fs = require('fs'), path = require('path');
module.exports = async ({ page, waitText, cfg }) => {
  const h = require('./lib.cjs')(page);
  const { must, until, menu } = h;
  const S = path.join(cfg, 'sessions');
  const exists = (p) => fs.existsSync(path.join(S, p));
  const row = (n) => page.locator(`.tree-row:has(.name:text-is("${n}"))`);
  const ctx = async (n, item) => {
    if (n) await row(n).click({ button: 'right' }); else await page.locator('.tree').click({ button: 'right', position: { x: 20, y: 250 } });
    await page.click(`.popup .item:has(.label:text-is("${item}"))`);
  };
  const popupLabels = async (n) => {
    await row(n).click({ button: 'right' });
    const l = await page.locator('.popup .item .label').allInnerTexts();
    await page.keyboard.press('Escape');
    await page.locator('.popup').waitFor({ state: 'detached' });
    return l;
  };
  const prompt = async (v) => { await page.locator('.dialog input').fill(v); await page.keyboard.press('Enter'); await page.locator('.dialog').waitFor({ state: 'detached' }); };
  const newLocal = async (name, viaFolder) => {
    await ctx(viaFolder, 'New Session…');
    await page.locator('.dialog .field:has(label:text-is("Name")) input').fill(name);
    await page.locator('.dialog .field:has(label:text-is("Protocol")) select').selectOption({ label: 'Local Shell' });
    await page.locator('.dialog footer button.primary:text-is("Create")').click();
    await page.locator('.dialog').waitFor({ state: 'detached' });
  };

  // ---- folder + sessions through the dialogs
  await ctx(null, 'New Folder…'); await prompt('Work');
  await until(() => exists('Work') && row('Work').count(), 'folder created');
  await newLocal('alpha', 'Work');
  await newLocal('beta', 'Work');
  await until(() => exists('Work/alpha.json') && exists('Work/beta.json'), 'session files');
  JSON.parse(fs.readFileSync(path.join(S, 'Work/alpha.json'), 'utf8'));
  await row('alpha').waitFor(); await row('beta').waitFor();

  // ---- filter
  const filter = page.locator('input[aria-label="Filter sessions"]');
  await filter.fill('alp');
  await until(async () => (await row('beta').count()) === 0 && (await row('alpha').count()) === 1, 'filter keeps matches');
  await filter.fill('zzz');
  await page.locator('.empty-hint:has-text("No sessions match")').waitFor();
  await filter.fill('');
  await row('beta').waitFor();

  // ---- context menus
  const sl = await popupLabels('alpha');
  for (const e of ['Connect', 'Connect SFTP', 'Properties…', 'Duplicate…', 'Rename…', 'Delete…', 'New Session…', 'New Folder…', 'Import Sessions…']) must(sl.includes(e), 'session menu lacks ' + e + ': ' + sl);
  const fl = await popupLabels('Work');
  for (const e of ['Connect All', 'Rename…', 'Delete…', 'New Session…', 'New Folder…']) must(fl.includes(e), 'folder menu lacks ' + e + ': ' + fl);
  must(!fl.includes('Connect'), 'folder has no plain Connect');

  // ---- rename, duplicate, move (by rename path), delete
  await ctx('beta', 'Rename…'); await prompt('gamma');
  await until(() => exists('Work/gamma.json') && !exists('Work/beta.json'), 'rename');
  await ctx('alpha', 'Duplicate…'); await prompt('Work/alpha2');
  await until(() => exists('Work/alpha2.json') && exists('Work/alpha.json'), 'duplicate');
  await ctx('alpha2', 'Rename…'); await prompt('Archive/old');
  await until(() => exists('Work/Archive/old.json') && !exists('Work/alpha2.json'), 'move by renaming with a path');
  await row('old').waitFor();
  await ctx('old', 'Delete…');
  await page.locator('.dialog[aria-label="Delete"]').waitFor();
  await page.locator('.dialog button:text-is("Cancel")').click();
  await page.locator('.dialog').waitFor({ state: 'detached' });
  must(exists('Work/Archive/old.json'), 'cancel keeps the session');
  await ctx('old', 'Delete…');
  await page.locator('.dialog button.danger').click();
  await until(() => !exists('Work/Archive/old.json'), 'session deleted');
  await ctx('Archive', 'Delete…');
  await page.locator('.dialog button.danger').click();
  await until(() => !exists('Work/Archive'), 'folder deleted');

  // ---- connect: double click, then Connect All
  await row('alpha').dblclick();
  await until(async () => (await h.tabs()) === 1, 'tab from double click');
  await waitText(/[$#] ?$/m);
  await page.locator('.tree-row:has(.name:text-is("alpha")) .dot').waitFor();
  await ctx('Work', 'Connect All');
  await until(async () => (await h.tabs()) === 3, 'Connect All opens one tab per session (alpha + gamma)');
  await until(async () => (await page.locator('.tab .state.connected, .tab .state.activity').count()) === 3, 'all connected');

  // ---- Quick Connect with "Save session"
  await menu('File', 'Quick Connect…');
  await page.locator('.dialog .field:has(label:text-is("Protocol")) select').selectOption({ label: 'Local Shell' });
  await page.locator('.dialog label.check:has-text("Save session")').click();
  await h.field('Session name').fill('qc/saved');
  await page.locator('.dialog footer button.primary').click();
  await until(() => exists('qc/saved.json'), 'saved from Quick Connect');
  await until(async () => (await h.tabs()) === 4, 'quick connect tab');
  await row('saved').waitFor();
  const sv = JSON.parse(fs.readFileSync(path.join(S, 'qc/saved.json'), 'utf8'));
  must(JSON.stringify(sv).includes('local'), 'saved protocol ' + JSON.stringify(sv));
};
