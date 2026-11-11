module.exports = async ({ page, shot, waitText }) => {
  await page.click('button[title="Local Shell"]');
  await waitText(/[$#] ?$/m);
  await page.keyboard.type('export LANG=C.UTF-8; clear; echo hello-$((40+2))\n');
  await waitText(/hello-42/);
  await page.keyboard.type('printf "\\033[1;31mbold red\\033[0m \\033[4;32munderline\\033[0m \\033[7minverse\\033[0m \\033[38;2;255;140;0mtruecolor\\033[0m\\n"; printf "\\342\\224\\214\\342\\224\\200\\342\\224\\254\\342\\224\\200\\342\\224\\220 \\342\\226\\210\\342\\226\\204\\342\\226\\221 \\344\\270\\255\\346\\226\\207\\n\\342\\224\\224\\342\\224\\200\\342\\224\\264\\342\\224\\200\\342\\224\\230\\n"\n');
  await page.keyboard.type('ls --color /usr | head -4; tput cols; tput lines\n');
  const s = await waitText(/\n\d+\n\d+\n/);
  console.log(s.split('\n').filter(Boolean).slice(-9).join('\n'));
  await page.keyboard.type('vim -u NONE /etc/hostname\n');
  await waitText(/hostname/);
  await page.keyboard.type(':set nu\n');
  await page.waitForTimeout(300);
  await shot('local-vim');
  await page.keyboard.type(':q!\n');
  await waitText(/tput lines/);
  await shot('local');
};
