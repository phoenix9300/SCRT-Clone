// Runs a Python script through Script → Run…: the script drives the shell,
// reads the real screen and reports it in a message box.
const fs = require('fs'), os = require('os'), nodePath = require('path');
const SCRIPT = `crt.Screen.Synchronous = True
crt.Screen.Send("echo e2e-$((6*7))\\r")
ok = crt.Screen.WaitForString("e2e-42", 10)
txt = crt.Screen.Get(1, 1, 10, 40)
crt.Dialog.MessageBox("SCREEN-OK=%s %s" % (ok, txt), "E2E")
print("finished")
`;
module.exports = async ({ page, shot, waitText }) => {
  const dir = fs.mkdtempSync(nodePath.join(os.tmpdir(), 'hawser-script-'));
  fs.writeFileSync(nodePath.join(dir, 'crt-e2e.py'), SCRIPT);
  await page.click('button[title="Local Shell"]');
  await waitText(/[$#] ?$/m);
  await page.click('.menubar >> text=Script');
  await page.click('text=Run…');
  const path = page.locator('input[aria-label="Path"]');
  await path.fill(dir);
  await path.press('Enter');
  await page.click('tr:has-text("crt-e2e.py")');
  await page.click('.dialog footer button:has-text("Run")');
  const dlg = page.locator('.dialog:has-text("SCREEN-OK")');
  await dlg.waitFor({ timeout: 15000 });
  const text = await dlg.innerText();
  console.log('DIALOG:', text.replace(/\s+/g, ' '));
  if (!/SCREEN-OK=True/.test(text) || !/e2e-42/.test(text)) throw new Error('dialog text wrong');
  await shot('script-py');
  await dlg.locator('button:has-text("OK")').click();
  await page.waitForSelector('.toast:has-text("finished")', { timeout: 5000 });
};
