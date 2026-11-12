// Shared helpers for the UI scenarios (not a scenario itself).
module.exports = (page) => {
  const h = {};
  h.must = (c, m) => { if (!c) throw new Error(m); };
  h.until = async (f, what, ms = 10000) => {
    const t = Date.now();
    for (;;) {
      let v; try { v = await f(); } catch (e) { v = false; }
      if (v) return v;
      if (Date.now() - t > ms) throw new Error('timeout: ' + what);
      await page.waitForTimeout(100);
    }
  };
  h.menu = async (title, item) => {
    await page.click(`.menu-title:text-is("${title}")`);
    await page.click(`.popup .item:has(.label:text-is("${item}"))`);
  };
  h.sub = async (title, item, subitem) => {
    await page.click(`.menu-title:text-is("${title}")`);
    await page.click(`.popup .item:has(.label:text-is("${item}"))`);
    await page.click(`.popup >> nth=-1 >> .item:has(.label:text-is("${subitem}"))`);
  };
  h.field = (l) => page.locator('.dialog').last().locator(`.field:has(label:text-is("${l}")) input`);
  h.tabs = () => page.locator('.tab').count();
  h.dialog = (t) => page.locator(`.dialog[aria-label="${t}"]`);
  h.okDialog = () => page.locator('.dialog').last().locator('footer button.primary').click();
  h.focusTerm = () => page.evaluate(() => window.__hawser.ctrl().focus());
  h.ctrlState = () => page.evaluate(() => { const t = window.__hawser.ctrl().term; return { rows: t.rows, cols: t.cols, x: t.buf.x, y: t.buf.y }; });
  /** opens a local shell tab and waits for its prompt */
  h.shell = async (waitText) => {
    const n = await h.tabs();
    await page.click('button[title="Local Shell"]');
    await h.until(async () => (await h.tabs()) === n + 1, 'tab count ' + (n + 1));
    await waitText(/[$#] ?$/m);
  };
  /** navigates a file chooser to a directory (waits for the first listing so it cannot overwrite the typed path) */
  h.goDir = async (dlg, dir) => {
    const p = dlg.locator('input[aria-label="Path"]');
    await h.until(async () => (await p.inputValue()) !== '', 'file chooser listing');
    await p.fill(dir); await p.press('Enter');
    await h.until(async () => (await p.inputValue()) === dir && (await dlg.locator('tr').count()) > 1, 'listing of ' + dir);
  };
  h.toastText = () => page.locator('.toast').allInnerTexts();
  return h;
};
