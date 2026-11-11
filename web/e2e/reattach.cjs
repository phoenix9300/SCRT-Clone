// Sessions belong to the engine: reloading the window re-attaches to them and
// replays their output. Reloads through the private launch page, which also
// checks that page hands over the access token.
module.exports = async ({ page, waitText, cfg }) => {
  await page.click('button[title="Local Shell"]');
  await waitText(/[$#] ?$/m);
  await page.keyboard.type('echo keep-$((40+2))\n');
  await waitText(/^keep-42/m);
  await page.evaluate(() => sessionStorage.clear());
  await page.goto('file://' + cfg + '/launch.html');
  await page.waitForSelector('.tab .state.connected', { timeout: 10000 });
  await waitText(/^keep-42/m);
  await page.locator('.term-root').click();
  await page.keyboard.type('echo again-$((1+1))\n');
  await waitText(/^again-2/m);
  if ((await page.locator('.tab').count()) !== 1) throw new Error('expected exactly one re-attached tab');
};
