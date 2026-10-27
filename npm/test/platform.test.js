'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { platformPackage } = require('../netivri/bin/netivri.js');

test('platformPackage maps the six supported platform/arch combos', () => {
  const cases = [
    ['linux', 'x64', false, '@phpcraftdream/netivri-linux-x64'],
    ['linux', 'arm64', false, '@phpcraftdream/netivri-linux-arm64'],
    ['darwin', 'x64', false, '@phpcraftdream/netivri-darwin-x64'],
    ['darwin', 'arm64', false, '@phpcraftdream/netivri-darwin-arm64'],
    ['win32', 'x64', false, '@phpcraftdream/netivri-w32-x64'],
    ['win32', 'arm64', false, '@phpcraftdream/netivri-w32-arm64'],
  ];
  for (const [platform, arch, isMusl, expected] of cases) {
    assert.equal(platformPackage(platform, arch, isMusl), expected,
      `${platform}-${arch} isMusl=${isMusl}`);
  }
});

test('platformPackage returns null for musl linux', () => {
  assert.equal(platformPackage('linux', 'x64', true), null);
  assert.equal(platformPackage('linux', 'arm64', true), null);
});

test('platformPackage returns null for unsupported platforms and arches', () => {
  assert.equal(platformPackage('freebsd', 'x64', false), null);
  assert.equal(platformPackage('sunos', 'arm64', false), null);
  assert.equal(platformPackage('openbsd', 'x64', false), null);
  assert.equal(platformPackage('aix', 'ppc64', false), null);
  assert.equal(platformPackage('linux', 'ia32', false), null);
  assert.equal(platformPackage('darwin', 'ppc', false), null);
  assert.equal(platformPackage('win32', 'ia32', false), null);
});
