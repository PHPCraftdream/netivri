'use strict';

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { buildPackages, parseArgs, PLATFORMS } = require('../scripts/build-packages.js');

const VERSION = '1.2.3';

function makeFakeArchives(root, { skip = [] } = {}) {
  fs.mkdirSync(path.join(root, 'archives'), { recursive: true });
  for (const p of PLATFORMS) {
    if (skip.includes(p.pkg)) continue;
    const dir = path.join(root, 'archives', `netivri_${VERSION}_${p.goos}_${p.goarch}`);
    writeFakeArchive(dir, p);
  }
  return path.join(root, 'archives');
}

function writeFakeArchive(dir, p) {
  fs.mkdirSync(path.join(dir, 'THIRD_PARTY_LICENSES'), { recursive: true });
  fs.writeFileSync(path.join(dir, p.bin), '#!/bin/sh\nexit 0\n');
  if (process.platform !== 'win32') {
    fs.chmodSync(path.join(dir, p.bin), 0o755);
  }
  fs.writeFileSync(path.join(dir, 'LICENSE'), 'GPL-3.0-only');
  fs.writeFileSync(path.join(dir, 'README.md'), 'netivri');
  fs.writeFileSync(path.join(dir, 'THIRD_PARTY_LICENSES', 'a.txt'), 'license text');
}

function tempRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'netivri-npm-build-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function listTree(dir) {
  const out = [];
  const walk = (d) => {
    for (const entry of fs.readdirSync(d, { withFileTypes: true }).sort((a, b) => a.name < b.name ? -1 : 1)) {
      const p = path.join(d, entry.name);
      out.push(p);
      if (entry.isDirectory()) walk(p);
    }
  };
  walk(dir);
  return out;
}

test('parseArgs and buildPackages agree on option names (CLI integration)', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const out = path.join(root, 'out');
  const args = parseArgs(['--version', VERSION, '--archive-dir', archives, '--out', out]);
  const summary = buildPackages(args);
  assert.equal(summary.ok, true);
  assert.equal(summary.version, VERSION);
  assert.equal(summary.packages.length, PLATFORMS.length + 1);
  assert.ok(fs.existsSync(path.join(out, 'netivri', 'bin', 'netivri.js')));
});

test('buildPackages generates the main package with substituted version', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const out = path.join(root, 'out');
  const summary = buildPackages({ version: VERSION, archiveDir: archives, out });

  assert.equal(summary.ok, true);
  assert.equal(summary.version, VERSION);
  const names = summary.packages.map((p) => p.name).sort();
  assert.deepEqual(names, [
    '@phpcraftdream/netivri',
    '@phpcraftdream/netivri-darwin-arm64',
    '@phpcraftdream/netivri-darwin-x64',
    '@phpcraftdream/netivri-linux-arm64',
    '@phpcraftdream/netivri-linux-x64',
    '@phpcraftdream/netivri-w32-arm64',
    '@phpcraftdream/netivri-w32-x64',
  ]);

  const main = JSON.parse(fs.readFileSync(path.join(out, 'netivri', 'package.json'), 'utf8'));
  assert.equal(main.name, '@phpcraftdream/netivri');
  assert.deepEqual(main.publishConfig, { access: 'public' });
  assert.equal(main.version, VERSION);
  assert.equal(main.license, 'GPL-3.0-only');
  assert.equal(main.bin.netivri, 'bin/netivri.js');
  assert.deepEqual(main.engines, { node: '>=18' });
  assert.equal(Object.keys(main.dependencies).length, 0);
  assert.deepEqual(Object.keys(main.optionalDependencies).sort(), [
    '@phpcraftdream/netivri-darwin-arm64',
    '@phpcraftdream/netivri-darwin-x64',
    '@phpcraftdream/netivri-linux-arm64',
    '@phpcraftdream/netivri-linux-x64',
    '@phpcraftdream/netivri-w32-arm64',
    '@phpcraftdream/netivri-w32-x64',
  ]);
  for (const version of Object.values(main.optionalDependencies)) {
    assert.equal(version, VERSION);
  }
  assert.ok(fs.existsSync(path.join(out, 'netivri', 'bin', 'netivri.js')));
  assert.ok(fs.existsSync(path.join(out, 'netivri', 'README.md')));
});

test('built launcher keeps the shebang and the executable bit', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const out = path.join(root, 'out');
  buildPackages({ version: VERSION, archiveDir: archives, out });

  const launcherPath = path.join(out, 'netivri', 'bin', 'netivri.js');
  const firstLine = fs.readFileSync(launcherPath, 'utf8').split('\n', 1)[0];
  assert.equal(firstLine, '#!/usr/bin/env node');
  if (process.platform !== 'win32') {
    const mode = fs.statSync(launcherPath).mode;
    assert.equal(mode & 0o111, 0o111, 'launcher keeps the executable bit');
  }
});

test('built main package contains LICENSE and THIRD_PARTY_LICENSES', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const out = path.join(root, 'out');
  buildPackages({ version: VERSION, archiveDir: archives, out });

  const mainDir = path.join(out, 'netivri');
  assert.equal(fs.readFileSync(path.join(mainDir, 'LICENSE'), 'utf8'), 'GPL-3.0-only');
  assert.equal(
    fs.readFileSync(path.join(mainDir, 'THIRD_PARTY_LICENSES', 'a.txt'), 'utf8'),
    'license text'
  );
});

test('buildPackages generates platform packages with os/cpu and files, and no libc restriction', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const out = path.join(root, 'out');
  buildPackages({ version: VERSION, archiveDir: archives, out });

  for (const p of PLATFORMS) {
    const pkgDir = path.join(out, p.pkg);
    const manifest = JSON.parse(fs.readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));
    assert.equal(manifest.name, p.name);
    assert.ok(p.name.startsWith('@phpcraftdream/'), `${p.name} is scoped`);
    assert.deepEqual(manifest.publishConfig, { access: 'public' });
    // Not for installing by hand: no keywords, and the description points to the main package.
    assert.equal(manifest.keywords, undefined, `${p.name} has no keywords`);
    assert.ok(manifest.description.includes('@phpcraftdream/netivri'), `${p.name} description`);
    assert.equal(manifest.version, VERSION);
    assert.deepEqual(manifest.os, [p.os]);
    assert.deepEqual(manifest.cpu, [p.cpu]);
    // The binary is static: no libc field, so glibc and musl hosts both get it.
    assert.equal(manifest.libc, undefined);
    assert.equal(manifest.license, 'GPL-3.0-only');

    assert.ok(fs.existsSync(path.join(pkgDir, 'bin', p.bin)), `${p.pkg} binary`);
    assert.ok(fs.existsSync(path.join(pkgDir, 'LICENSE')));
    assert.ok(fs.existsSync(path.join(pkgDir, 'THIRD_PARTY_LICENSES', 'a.txt')));

    if (process.platform !== 'win32') {
      const mode = fs.statSync(path.join(pkgDir, 'bin', p.bin)).mode;
      assert.equal(mode & 0o111, 0o111, `${p.pkg} binary keeps the executable bit`);
    }
  }
});

test('buildPackages works from a single extracted archive directory', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const single = path.join(archives, `netivri_${VERSION}_linux_amd64`);
  const out = path.join(root, 'out');
  const summary = buildPackages({ version: VERSION, archiveDir: single, out });

  assert.equal(summary.ok, true);
  const names = summary.packages.map((p) => p.name).sort();
  assert.deepEqual(names, ['@phpcraftdream/netivri', '@phpcraftdream/netivri-linux-x64']);
  assert.ok(fs.existsSync(path.join(out, 'netivri-linux-x64', 'bin', 'netivri')));
  // The main package still advertises all six platform packages.
  const main = JSON.parse(fs.readFileSync(path.join(out, 'netivri', 'package.json'), 'utf8'));
  assert.equal(Object.keys(main.optionalDependencies).length, PLATFORMS.length);
  for (const version of Object.values(main.optionalDependencies)) {
    assert.equal(version, VERSION);
  }
});

test('buildPackages rejects an archive dir with no platform directories', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root, { skip: PLATFORMS.map((p) => p.pkg) });
  const out = path.join(root, 'out');
  assert.throws(
    () => buildPackages({ version: VERSION, archiveDir: archives, out }),
    /no extracted netivri_<version>_<os>_<arch> directories/
  );
});

test('semver validation accepts valid versions and rejects invalid ones', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const out = path.join(root, 'out');
  const attempt = (version) => () =>
    buildPackages({ version, archiveDir: archives, out: path.join(out, version) });

  for (const good of ['1.2.3', '0.0.0', '0.0.0-dryrun', '1.2.3-rc.1', '1.2.3-alpha-1', '10.20.30']) {
    assert.doesNotThrow(attempt(good), `should accept ${good}`);
  }
  for (const bad of ['1.2', 'v1.2.3', '01.2.3', '1.02.3', '1.2.3-rc..1', '1.2.3-rc.',
    '1.2.3-01', '1.2.3-', 'abc', '', '../evil']) {
    assert.throws(attempt(bad), /invalid version/, `should reject "${bad}"`);
  }
});

test('buildPackages rejects a missing binary file', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  fs.rmSync(path.join(archives, `netivri_${VERSION}_linux_arm64`, 'netivri'));
  const out = path.join(root, 'out');
  assert.throws(
    () => buildPackages({ version: VERSION, archiveDir: archives, out }),
    /binary for linux\/arm64/
  );
});

test('buildPackages writes nothing outside --out', (t) => {
  const root = tempRoot(t);
  const archives = makeFakeArchives(root);
  const out = path.join(root, 'out');

  // Snapshot the temp tree BEFORE the first build (only the fake archives
  // exist), then require that everything outside --out is unchanged after
  // the first build and after a rebuild.
  const before = listTree(root);
  const outside = () => listTree(root).filter((p) => p !== out && !p.startsWith(out + path.sep));
  buildPackages({ version: VERSION, archiveDir: archives, out });
  assert.deepEqual(outside(), before);
  buildPackages({ version: '2.0.0', archiveDir: archives, out });
  assert.deepEqual(outside(), before);
  assert.equal(JSON.parse(
    fs.readFileSync(path.join(out, 'netivri', 'package.json'), 'utf8')
  ).version, '2.0.0');
});

test('parseArgs requires and routes all three arguments', () => {
  assert.deepEqual(
    parseArgs(['--version', '1.0.0', '--archive-dir', 'a', '--out', 'o']),
    { version: '1.0.0', archiveDir: 'a', out: 'o' }
  );
  assert.throws(() => parseArgs(['--version', '1.0.0']), /missing required argument --archive-dir/);
  assert.throws(() => parseArgs(['--version', '1.0.0', '--archive-dir', 'a']), /missing required argument --out/);
  assert.throws(() => parseArgs(['--version']), /missing value for --version/);
  assert.throws(() => parseArgs(['--version', '1.0.0', '--archive-dir', 'a', '--out', 'o', '--bogus']), /unknown argument --bogus/);
});
