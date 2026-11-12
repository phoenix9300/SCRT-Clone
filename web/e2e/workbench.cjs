// Workbench: tabs, tab groups, locking, command window, button bar, find bar,
// keyword highlighting, View toggles, theme, zoom and the status bar.
module.exports = async ({ page, shot, waitText }) => {
  const h = require('./lib.cjs')(page);
  const { must, until, menu } = h;
  const type = (s) => page.keyboard.type(s + '\n');
  const titles = () => page.locator('.tab .title').allInnerTexts();

  // ---- two shells, switching, rename
  await h.shell(waitText);
  await h.shell(waitText);
  await type('echo tabB$((1+1))'); await waitText(/^tabB2/m);
  await page.keyboard.press('Control+Tab');
  await until(async () => (await page.locator('.tab').first().getAttribute('class')).includes('active'), 'Ctrl+Tab -> first tab');
  await type('echo tabA$((1+2))'); await waitText(/^tabA3/m);
  must(!/tabB2/.test((await page.evaluate(() => window.__hawser.screen())).join('\n')), 'tab A shows tab B output');
  await page.keyboard.press('Alt+2');
  await until(async () => (await page.locator('.tab').nth(1).getAttribute('class')).includes('active'), 'Alt+2');
  await waitText(/^tabB2/m);
  await page.keyboard.press('Alt+1');
  await until(async () => (await page.locator('.tab').first().getAttribute('class')).includes('active'), 'Alt+1');
  await waitText(/^tabA3/m);
  await menu('Window', 'Rename Tab…');
  await page.locator('.dialog input').fill('Alpha'); await page.keyboard.press('Enter');
  await until(async () => (await titles())[0] === 'Alpha', 'tab renamed');
  await page.keyboard.press('Alt+2');
  await menu('Window', 'Rename Tab…');
  await page.locator('.dialog input').fill('Beta'); await page.keyboard.press('Enter');
  await until(async () => (await titles())[1] === 'Beta', 'tab 2 renamed');

  // ---- tab groups: right, below, close removes the group
  await menu('Window', 'Move to New Tab Group (Right)');
  await until(async () => (await page.locator('.group').count()) === 2, 'two groups');
  must(await page.locator('.groups.split-row').count() === 1, 'split-row class');
  await type('echo gB$((2*3))'); await waitText(/^gB6/m);
  await page.locator('.group').first().locator('.tab', { hasText: 'Alpha' }).click();
  await h.focusTerm();
  await type('echo gA$((2*4))'); await waitText(/^gA8/m);
  await h.shell(waitText); // third tab in group 1
  await menu('Window', 'Move to New Tab Group (Below)');
  await until(async () => (await page.locator('.group').count()) === 3, 'three groups');
  must(await page.locator('.groups.split-column').count() === 1, 'split-column class');
  await type('echo gC$((3*3))'); await waitText(/^gC9/m);
  // close confirmation: cancel keeps the tab and the group, confirm closes both
  await page.locator('.group.focus .tab .x').click();
  await page.locator('.dialog[aria-label="Close session"]').waitFor();
  await page.locator('.dialog button:text-is("Cancel")').click();
  await page.locator('.dialog').waitFor({ state: 'detached' });
  must((await h.tabs()) === 3 && (await page.locator('.group').count()) === 3, 'cancel must keep the tab');
  await page.locator('.group.focus .tab .x').click();
  await page.locator('.dialog button:text-is("Close")').click();
  await until(async () => (await page.locator('.group').count()) === 2 && (await h.tabs()) === 2, 'group removed with its last tab');

  // ---- status bar: cursor + size follow the terminal
  await page.locator('.group').first().locator('.tab', { hasText: 'Alpha' }).click();
  await h.focusTerm();
  await type('echo pos$((1+1))'); await waitText(/^pos2/m);
  await until(async () => {
    const s = await h.ctrlState();
    const cells = await page.locator('.statusbar .cell').allInnerTexts();
    return cells.includes(`${s.y + 1}, ${s.x + 1}`) && cells.includes(`${s.rows} Rows, ${s.cols} Cols`);
  }, 'status bar cursor/size');

  // ---- command window to all sessions
  await menu('View', 'Command Window');
  await page.locator('select[aria-label="Send commands to"]').selectOption('all');
  await page.locator('textarea[aria-label="Command window"]').fill('echo all$((3+4))');
  await page.keyboard.press('Enter');
  await waitText(/^all7/m);
  await page.locator('.group').nth(1).locator('.tab').click();
  await waitText(/^all7/m);
  await page.locator('.group').first().locator('.tab').click();
  await menu('View', 'Command Window');
  must(await page.locator('.cmdwin').count() === 0, 'command window hidden');

  // ---- button bar from the editor
  await menu('Options', 'Button Bar…');
  await page.locator('.dialog button:text-is("Add button")').click();
  await page.locator('.dialog input[placeholder="Label"]').fill('Say');
  await page.locator('.dialog input[placeholder^="text"]').fill('echo btn$((5*5))\\r');
  await h.okDialog();
  await menu('View', 'Button Bar');
  await page.locator('.buttonbar button:text-is("Say")').click();
  await waitText(/^btn25/m);

  // ---- find bar
  await h.focusTerm();
  await type('echo NeedleXyz; seq 1 200');
  await waitText(/^200/m);
  await page.keyboard.press('Control+Shift+F');
  const find = page.locator('.findbar input[aria-label="Find"]');
  await find.waitFor();
  await find.fill('needlexyz'); await find.press('Enter');
  await page.locator('.statusbar .cell.on:has-text("SCROLL")').waitFor({ timeout: 5000 }); // jumped back into scrollback
  must(!(await find.getAttribute('class')).includes('miss'), 'needle should be found');
  await page.locator('.findbar button[title="Match case"]').click();
  await find.press('Enter');
  await until(async () => (await find.getAttribute('class')).includes('miss'), 'case-sensitive search misses');
  await page.locator('.findbar button[title="Match case"]').click();
  await find.fill('zzzqqq'); await find.press('Enter');
  await until(async () => (await find.getAttribute('class')).includes('miss'), 'no-match state');
  await find.press('Escape');
  must(await page.locator('.findbar').count() === 0, 'find bar closes');

  // ---- keyword highlighting for a saved session
  await page.evaluate(() => window.__hawser.rpc('sessions.save', { path: 'kw', overrides: { protocol: 'local' }, secrets: {} }));
  await page.locator('.tree-row:has-text("kw")').dblclick();
  await until(async () => (await h.tabs()) === 3, 'kw tab');
  await waitText(/[$#] ?$/m);
  const deco = (re) => page.evaluate((src) => {
    const c = window.__hawser.ctrl(), b = c.term.buf, rows = c.term.screenText();
    const r = rows.findIndex((l) => new RegExp(src).test(l));
    if (r < 0) return 'norow';
    return c.decorations(b.lines[b.viewportTop + r], b.trimmed + b.viewportTop + r);
  }, re.source);
  await type('clear; echo "error at 10.1.2.3"'); await waitText(/^error at/m);
  must((await deco(/^error at/)) === null, 'no decorations before enabling');
  await menu('Options', 'Session Options…');
  await page.locator('.dialog nav .nav-item:text-is("Appearance")').click();
  await page.locator('.dialog .field:has(label:text-is("Keyword set")) select').selectOption('Network');
  await h.okDialog();
  await page.locator('.dialog').waitFor({ state: 'detached' });
  await until(async () => { const d = await deco(/^error at/); return Array.isArray(d) && d.length >= 2; }, 'keyword decorations');
  const d = await deco(/^error at/);
  must(d.some((x) => x.bold && x.start === 0 && x.end >= 4) && d.some((x) => x.start === 9), 'decoration ranges ' + JSON.stringify(d));

  // ---- lock / unlock
  await h.focusTerm();
  await menu('File', 'Lock Session…');
  await page.locator('.dialog input').fill('pw'); await page.keyboard.press('Enter');
  await page.locator('.overlay-msg:has-text("Session locked")').waitFor();
  await h.focusTerm();
  await type('echo lk$((4+4))');
  await page.waitForTimeout(600);
  must(!/lk8|echo lk/.test((await page.evaluate(() => window.__hawser.screen())).join('\n')), 'typing must be blocked while locked');
  await menu('File', 'Unlock Session…');
  await page.locator('.dialog input').fill('bad'); await page.keyboard.press('Enter');
  await page.locator('.toast:has-text("Incorrect password")').waitFor();
  must(await page.locator('.overlay-msg:has-text("Session locked")').count() === 1, 'still locked after a bad password');
  await menu('File', 'Unlock Session…');
  await page.locator('.dialog input').fill('pw'); await page.keyboard.press('Enter');
  await page.locator('.overlay-msg').waitFor({ state: 'detached' });
  await h.focusTerm();
  await type('echo lk$((4+4))'); await waitText(/^lk8/m);

  // ---- View toggles
  for (const [item, sel] of [['Session Manager', '.sidebar'], ['Toolbar', '.toolbar'], ['Status Bar', '.statusbar']]) {
    await menu('View', item);
    await until(async () => (await page.locator(sel).count()) === 0, item + ' hidden');
    await menu('View', item);
    await until(async () => (await page.locator(sel).count()) === 1, item + ' shown');
  }

  // ---- zoom
  await h.focusTerm();
  const z0 = await h.ctrlState();
  await menu('View', 'Zoom In'); await menu('View', 'Zoom In'); await menu('View', 'Zoom In');
  await until(async () => { const z = await h.ctrlState(); return z.rows < z0.rows && z.cols < z0.cols; }, 'zoom in shrinks the grid');
  for (let i = 0; i < 7; i++) await menu('View', 'Zoom Out');
  await until(async () => { const z = await h.ctrlState(); return z.rows > z0.rows && z.cols > z0.cols; }, 'zoom out grows the grid');
  await menu('View', 'Reset Zoom');
  await until(async () => { const z = await h.ctrlState(); return z.rows === z0.rows && z.cols === z0.cols; }, 'zoom reset');

  // ---- theme
  await h.sub('View', 'Theme', 'Light');
  await until(async () => (await page.evaluate(() => document.documentElement.dataset.theme)) === 'light', 'light theme');
  await shot('workbench-light');
  await h.sub('View', 'Theme', 'Dark');
  await until(async () => (await page.evaluate(() => document.documentElement.dataset.theme)) === 'dark', 'dark theme');
};
