'use strict';

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {
  ERROR_CODE,
  detectMusl,
  errorEnvelope,
  resolveBinary,
  run,
} = require('../netivri/bin/netivri.js');

test('errorEnvelope matches the CLI JSON error contract', () => {
  const envelope = errorEnvelope('some message', 'some hint');
  assert.deepEqual(
    JSON.parse(JSON.stringify(envelope)),
    {
      schema_version: 1,
      ok: false,
      error: { code: 'INSTALL_BROKEN', message: 'some message', hint: 'some hint' },
    }
  );
  assert.equal(ERROR_CODE, 'INSTALL_BROKEN');
});

test('detectMusl returns a boolean for any platform', () => {
  assert.equal(typeof detectMusl('win32'), 'boolean');
  assert.equal(detectMusl('win32'), false);
  assert.equal(typeof detectMusl('linux'), 'boolean');
});

test('resolveBinary reports unsupported platforms with the error envelope fields', () => {
  const res = resolveBinary('freebsd', 'x64', false, () => '/unused');
  assert.ok(res.error);
  assert.equal(res.bin, undefined);
  const envelope = errorEnvelope(res.error.message, res.error.hint);
  assert.equal(envelope.error.code, ERROR_CODE);
  assert.ok(envelope.error.message.includes('freebsd-x64'));
  assert.ok(typeof envelope.error.hint === 'string' && envelope.error.hint.length > 0);

  const musl = resolveBinary('linux', 'x64', true, () => '/unused');
  assert.ok(musl.error);
  assert.ok(musl.error.message.includes('musl'));
});

test('resolveBinary reports a missing platform package', () => {
  const res = resolveBinary('linux', 'x64', false, () => {
    throw new Error("Cannot find module 'netivri-linux-x64/package.json'");
  });
  assert.ok(res.error);
  const envelope = errorEnvelope(res.error.message, res.error.hint);
  assert.equal(envelope.error.code, ERROR_CODE);
  assert.ok(envelope.error.message.includes('netivri-linux-x64'));
  assert.ok(envelope.error.hint.length > 0);
});

test('resolveBinary resolves the binary next to the platform package.json', () => {
  const pkgDir = path.join('fake', 'node_modules', 'netivri-darwin-arm64');
  const res = resolveBinary('darwin', 'arm64', false,
    () => path.join(pkgDir, 'package.json'));
  assert.equal(res.error, undefined);
  const expected = path.join(pkgDir, 'bin', 'netivri');
  assert.equal(res.bin, expected);
});

test('resolveBinary appends .exe on win32', () => {
  const pkgDir = path.join('fake', 'netivri-win32-x64');
  const res = resolveBinary('win32', 'x64', false,
    () => path.join(pkgDir, 'package.json'));
  assert.equal(res.bin, path.join(pkgDir, 'bin', 'netivri.exe'));
});

// The remaining tests spawn a tiny fake "binary" through the exported
// run() helper: process.execPath (a real executable on every OS) with a
// generated script, so no platform package is needed. Parent-signal
// forwarding (SIGINT/SIGTERM/SIGHUP relayed from this process to the
// child) is deliberately untested: it cannot be exercised hermetically.

function makeFakeScript(dir, body) {
  const file = path.join(dir, `fake-${Math.random().toString(36).slice(2)}.js`);
  fs.writeFileSync(file, body);
  return file;
}

test('run returns the child exit code 0', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'netivri-launcher-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const fake = makeFakeScript(dir, 'process.exit(0);\n');
  const code = await run(process.execPath, [fake], { stdio: 'ignore' });
  assert.equal(code, 0);
});

test('run returns a non-zero child exit code', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'netivri-launcher-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const fake = makeFakeScript(dir, 'process.exit(7);\n');
  const code = await run(process.execPath, [fake], { stdio: 'ignore' });
  assert.equal(code, 7);
});

test('run passes child stdout and stderr through', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'netivri-launcher-'));
  // Statement lines joined with real newlines. nl is built without a
  // literal backslash (String.fromCharCode(92)) so the generated file
  // contains the escape sequence backslash-n inside each single-quoted
  // string, keeping every statement on one line.
  const nl = String.fromCharCode(92) + 'n';
  const lines = [
    `process.stdout.write('out-line${nl}');`,
    `process.stderr.write('err-line${nl}');`,
  ];
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const fake = makeFakeScript(dir, lines.join(String.fromCharCode(10)) + String.fromCharCode(10));
  const pending = run(process.execPath, [fake], { stdio: ["ignore", "pipe", "pipe"] });
  const child = pending.child;
  assert.ok(child, 'run exposes the child handle');
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', (d) => { stdout += d; });
  child.stderr.on('data', (d) => { stderr += d; });
  const code = await pending;
  assert.equal(code, 0);
  assert.ok(stdout.includes('out-line'), `stdout missing marker: ${JSON.stringify(stdout)}`);
  assert.ok(stderr.includes('err-line'), `stderr missing marker: ${JSON.stringify(stderr)}`);
});

test('run maps a child killed by a signal to 128+signal (posix only)', async (t) => {
  if (process.platform === 'win32') {
    t.skip('signal exit codes are not portable on Windows');
    return;
  }
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'netivri-launcher-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const fake = makeFakeScript(dir, "process.kill(process.pid, 'SIGTERM');\n");
  const code = await run(process.execPath, [fake], { stdio: 'ignore' });
  assert.equal(code, 128 + 15);
});

test('run resolves 1 when the binary cannot be started', async (t) => {
  const missing = path.join(os.tmpdir(), 'netivri-does-not-exist-9f3a');
  const code = await run(missing, [], { stdio: 'ignore' });
  assert.equal(code, 1);
});
