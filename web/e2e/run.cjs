// End-to-end harness: starts a private Hawser engine with a throw-away
// configuration directory and drives the UI with Playwright.
//
//   node e2e/run.cjs <scenario>        (scenarios are the other files here)
//
// Environment: HAWSER_BIN (default ../hawser, built with `make build`),
// HAWSER_UI (serve the UI from a directory instead of the embedded bundle),
// DEBUG_WS=1 (log WebSocket frames).
const { chromium } = require('playwright');
const { spawn } = require('child_process');
const fs = require('fs'), path = require('path'), os = require('os');
const S = path.resolve(__dirname, '..', '..');
const OUT = process.env.E2E_OUT || path.join(os.tmpdir(), 'hawser-e2e-out');
fs.mkdirSync(OUT, { recursive: true });
const scenario = process.argv[2] || 'local';
(async () => {
  const cfg = fs.mkdtempSync(path.join(os.tmpdir(), 'hawser-e2e-'));
  const args = ['--no-browser', '--new-instance', '--config-dir', cfg]; if (process.env.HAWSER_UI) args.push('--ui-dir', process.env.HAWSER_UI);
  const proc = spawn(process.env.HAWSER_BIN || path.join(S, 'hawser'), args, { env: { ...process.env, SHELL: '/bin/bash' } });
  let out = '';
  proc.stdout.on('data', (d) => (out += d));
  proc.stderr.on('data', (d) => process.stderr.write('[hawser] ' + d));
  for (let i = 0; i < 50 && !/UI:\s+(http\S+)/.test(out); i++) await new Promise((r) => setTimeout(r, 100));
  const url = /UI:\s+(http\S+)/.exec(out)[1];
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 });
  const errors = [];
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.on('console', (m) => m.type() === 'error' && errors.push('console: ' + m.text()));
  if (process.env.DEBUG_WS) page.on('websocket', (ws) => {
    ws.on('framesent', (f) => console.log('>>', typeof f.payload === 'string' ? f.payload.slice(0, 120) : 'BIN ' + JSON.stringify(Buffer.from(f.payload).toString('latin1').slice(0, 60))));
    ws.on('framereceived', (f) => console.log('<<', typeof f.payload === 'string' ? f.payload.slice(0, 100) : 'BIN ' + JSON.stringify(Buffer.from(f.payload).toString('latin1').slice(0, 60))));
  });
  await page.goto(url);
  await page.waitForSelector('.menubar', { timeout: 10000 });
  const shot = (n) => page.screenshot({ path: path.join(OUT, n + '.png') });
  const screen = () => page.evaluate(() => window.__hawser?.screen() ?? []);
  const waitText = async (re, ms = 8000) => {
    const t0 = Date.now();
    while (Date.now() - t0 < ms) {
      const s = (await screen()).join('\n');
      if (re.test(s)) return s;
      await page.waitForTimeout(100);
    }
    throw new Error('timeout waiting for ' + re + '\n--- screen ---\n' + (await screen()).join('\n'));
  };
  // starts a private OpenSSH server (key authentication as the current user)
  const kids = [];
  const sshd = async () => {
    const { execFileSync } = require('child_process');
    const d = fs.mkdtempSync(path.join(os.tmpdir(), 'hawser-sshd-'));
    for (const k of ['hostkey', 'client']) execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(d, k)]);
    fs.copyFileSync(path.join(d, 'client.pub'), path.join(d, 'authorized_keys'));
    const port = 20000 + Math.floor(Math.random() * 20000);
    fs.writeFileSync(path.join(d, 'sshd_config'), `Port ${port}\nListenAddress 127.0.0.1\nHostKey ${d}/hostkey\nPidFile ${d}/pid\nAuthorizedKeysFile ${d}/authorized_keys\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nSubsystem sftp internal-sftp\nLogLevel ERROR\n`);
    fs.mkdirSync('/run/sshd', { recursive: true });
    const bin = fs.existsSync('/usr/sbin/sshd') ? '/usr/sbin/sshd' : 'sshd';
    kids.push(spawn(bin, ['-f', path.join(d, 'sshd_config'), '-D', '-e'], { stdio: 'ignore' }));
    await new Promise((r) => setTimeout(r, 700));
    return { port, clientKey: path.join(d, 'client'), user: os.userInfo().username };
  };
  try {
    await require(path.resolve(scenario.endsWith('js') ? scenario : path.join(__dirname, scenario + '.cjs')))({ page, shot, screen, waitText, cfg, S, sshd });
    console.log('SCENARIO OK:', scenario);
  } catch (e) {
    console.log('SCENARIO FAILED:', scenario, '\n' + e.message);
    await shot('fail-' + scenario);
    process.exitCode = 1;
  }
  if (errors.length) console.log('BROWSER ERRORS:\n' + errors.slice(0, 10).join('\n'));
  await browser.close();
  proc.kill();
  kids.forEach((k) => k.kill());
  fs.rmSync(cfg, { recursive: true, force: true });
})();
