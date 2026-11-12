// Drag and drop: sessions between folders in the session manager, tabs
// between tab groups.
module.exports = async ({ page, waitText }) => {
  const h = require('./lib.cjs')(page);
  const { must, until } = h;
  const rpc = (m, p) => page.evaluate(([m, p]) => window.__hawser.rpc(m, p), [m, p]);
  await rpc('sessions.mkdir', { path: 'Lab' });
  await rpc('sessions.save', { path: 'alpha', overrides: { host: '192.0.2.1' }, secrets: { password: 'pw' } });
  await rpc('sessions.save', { path: 'Lab/beta', overrides: { host: '192.0.2.2' }, secrets: {} });
  const row = (n) => page.locator(`.tree-row:has(.name:text-is("${n}"))`);
  await row('alpha').waitFor();
  // session → folder
  await row('alpha').dragTo(row('Lab'));
  await until(async () => JSON.stringify(await rpc('sessions.tree')).includes('"path":"Lab/alpha"'), 'session moved into folder');
  must((await rpc('sessions.get', { path: 'Lab/alpha' })).secrets.password === true, 'password did not follow the move');
  // session → root (drop on the empty part of the tree)
  await row('beta').dragTo(page.locator('.tree'), { targetPosition: { x: 40, y: 300 } });
  await until(async () => JSON.stringify(await rpc('sessions.tree')).includes('"path":"beta"'), 'session moved to the root');

  // tabs between groups
  await h.shell(waitText);
  await page.click('button[title="Local Shell"]');
  await until(async () => (await h.tabs()) === 2, 'second tab');
  await h.menu('Window', 'Move to New Tab Group (Right)');
  await until(async () => (await page.locator('.group').count()) === 2, 'two groups');
  const g = (i) => page.locator('.group').nth(i);
  must((await g(0).locator('.tab').count()) === 1 && (await g(1).locator('.tab').count()) === 1, 'one tab per group expected');
  // drag the first group's tab onto the second group's strip: the empty group disappears
  await g(0).locator('.tab').first().dragTo(g(1).locator('.tabstrip'), { targetPosition: { x: 400, y: 15 } });
  await until(async () => (await page.locator('.group').count()) === 1 && (await h.tabs()) === 2, 'tab moved, empty group removed');
  // reorder within the strip: drop the second tab on the first
  const before = await page.locator('.tab .title').allInnerTexts();
  await page.keyboard.type('');
  await page.locator('.tab').nth(1).dragTo(page.locator('.tab').nth(0));
  await page.waitForTimeout(300);
  must((await h.tabs()) === 2, 'tabs lost while reordering ' + before);
  // both terminals still work
  for (let i = 0; i < 2; i++) {
    await page.locator('.tab').nth(i).click();
    await h.focusTerm();
    await page.keyboard.type(`echo dnd-${i}-$((2+3))\n`);
    await waitText(new RegExp(`^dnd-${i}-5`, 'm'));
  }
};
