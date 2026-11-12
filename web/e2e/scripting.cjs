// Script menu: record -> generate -> save -> run again, error dialog, cancel.
const fs = require('fs'), path = require('path'), os = require('os');
module.exports = async ({ page, waitText }) => {
  const h = require('./lib.cjs')(page);
  const { must, until, menu } = h;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'scripting-'));
  const type = (s) => page.keyboard.type(s + '\n');
  await h.shell(waitText);

  // ---- record two commands
  await menu('Script', 'Start Recording Script');
  await page.locator('.statusbar .cell.rec').waitFor();
  await h.focusTerm();
  await type('echo rec$((1+1))'); await waitText(/^rec2/m);
  await type('echo rec$((2+2))'); await waitText(/^rec4/m);
  await menu('Script', 'Stop Recording Script…');
  const lang = page.locator('.dialog[aria-label="Stop Recording"]');
  await lang.locator('select').selectOption('python');
  await lang.locator('button.primary:text-is("Generate")').click();
  const view = page.locator('.dialog[aria-label="Recorded Script"]');
  await view.waitFor();
  const script = await view.locator('pre').innerText();
  must(script.includes('echo rec$((1+1))') && script.includes('echo rec$((2+2))'), 'recorded script lacks the commands:\n' + script);
  await page.locator('.statusbar .cell.rec').waitFor({ state: 'detached' });

  // ---- save it
  await view.locator('button:text-is("Save As…")').click();
  const sv = page.locator('.dialog[aria-label="Save Script"]');
  await h.goDir(sv, dir);
  await sv.locator('.row:has(.dim:text-is("File name")) input').fill('rec.py');
  await sv.locator('footer button.primary').click();
  await until(() => fs.existsSync(path.join(dir, 'rec.py')), 'script saved');
  must(fs.readFileSync(path.join(dir, 'rec.py'), 'utf8') === script.replace(/\n?$/, '\n') || fs.readFileSync(path.join(dir, 'rec.py'), 'utf8').includes('echo rec$((2+2))'), 'saved content');
  await view.locator('footer button.primary:text-is("Close")').click();

  const runScript = async (file) => {
    await menu('Script', 'Run…');
    const d = page.locator('.dialog[aria-label="Run Script"]');
    await h.goDir(d, dir);
    await d.locator(`tr:has-text("${file}")`).click();
    await d.locator('footer button.primary').click();
  };

  // ---- run the recording again against the same session
  await menu('Edit', 'Clear Screen and Scrollback');
  await page.waitForTimeout(300);
  must(!/^rec2/m.test((await page.evaluate(() => window.__hawser.screen())).join('\n')), 'screen cleared');
  await h.focusTerm();
  await type(''); await waitText(/[$#] ?$/m);
  await runScript('rec.py');
  // like SecureCRT, WaitForString only sees output that arrives after it started:
  // nudge the shell so that it prints a fresh prompt for the script to wait for
  await page.locator('.toast:has-text("Running rec.py")').waitFor();
  for (let i = 0; i < 20; i++) {
    await h.focusTerm();
    await type('');
    await page.waitForTimeout(400);
    if (/^rec4/m.test((await page.evaluate(() => window.__hawser.screen())).join('\n'))) break;
  }
  await waitText(/^rec2/m);
  await waitText(/^rec4/m);
  await page.locator('.toast:has-text("rec.py finished")').waitFor({ timeout: 15000 });

  // ---- a failing script shows the error dialog
  fs.writeFileSync(path.join(dir, 'bad.py'), 'print("before")\nraise RuntimeError("boom-e2e")\n');
  await runScript('bad.py');
  const err = page.locator('.dialog[aria-label="Script error: bad.py"]');
  await err.waitFor({ timeout: 15000 });
  const et = await err.innerText();
  must(/boom-e2e/.test(et), 'error text missing: ' + et);
  await err.locator('footer button.primary').click();

  // ---- a long-running script can be cancelled
  fs.writeFileSync(path.join(dir, 'sleep.py'), 'crt.Sleep(60000)\n');
  await runScript('sleep.py');
  await page.locator('.toast:has-text("Running sleep.py")').waitFor();
  await menu('Script', 'Cancel');
  await page.locator('.toast:has-text("Script sleep.py stopped")').waitFor({ timeout: 10000 });
  must((await page.locator('.toast:has-text("Running sleep.py")').count()) === 0, 'running toast removed');
};
