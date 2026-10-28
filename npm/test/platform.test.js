'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { platformPackage } = require('../netivri/bin/netivri.js');

test('platformPackage maps the six supported platform/arch combos', () => {
  const cases = [
    ['linux', 'x64', '@netivri/proxy-linux-x64'],
    ['linux', 'arm64', '@netivri/proxy-linux-arm64'],
    ['darwin', 'x64', '@netivri/proxy-darwin-x64'],
    ['darwin', 'arm64', '@netivri/proxy-darwin-arm64'],
    ['win32', 'x64', '@netivri/proxy-w32-x64'],
    ['win32', 'arm64', '@netivri/proxy-w32-arm64'],
  ];
  for (const [platform, arch, expected] of cases) {
    assert.equal(platformPackage(platform, arch), expected, `${platform}-${arch}`);
  }
});

test('platformPackage returns null for unsupported platforms and arches', () => {
  assert.equal(platformPackage('freebsd', 'x64'), null);
  assert.equal(platformPackage('sunos', 'arm64'), null);
  assert.equal(platformPackage('openbsd', 'x64'), null);
  assert.equal(platformPackage('aix', 'ppc64'), null);
  assert.equal(platformPackage('linux', 'ia32'), null);
  assert.equal(platformPackage('darwin', 'ppc'), null);
  assert.equal(platformPackage('win32', 'ia32'), null);
});
