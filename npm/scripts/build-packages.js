'use strict';

// Builds the netivri npm packages (main + one package per platform found)
// from extracted release archive directories. Node >= 18 built-ins only;
// never runs npm, never reads tokens, never writes outside --out.
//
// Usage:
//   node build-packages.js --version X.Y.Z --archive-dir <dir> --out <dir>
// --archive-dir either holds several extracted directories
// (netivri_<version>_<goos>_<goarch>) or is one such directory itself.

const fs = require('fs');
const path = require('path');

// Official semver shape without build metadata: numeric identifiers carry
// no leading zeros, prerelease identifiers are alphanumeric-or-hyphen and
// purely numeric ones carry no leading zeros.
const SEMVER_RE = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?$/;

// goos/goarch are the Go names used in the archive directory names;
// os/cpu are the npm field values.
const PLATFORMS = [
  { goos: 'linux', goarch: 'amd64', os: 'linux', cpu: 'x64', bin: 'netivri', libc: ['glibc'], pkg: 'netivri-linux-x64' },
  { goos: 'linux', goarch: 'arm64', os: 'linux', cpu: 'arm64', bin: 'netivri', libc: ['glibc'], pkg: 'netivri-linux-arm64' },
  { goos: 'darwin', goarch: 'amd64', os: 'darwin', cpu: 'x64', bin: 'netivri', libc: null, pkg: 'netivri-darwin-x64' },
  { goos: 'darwin', goarch: 'arm64', os: 'darwin', cpu: 'arm64', bin: 'netivri', libc: null, pkg: 'netivri-darwin-arm64' },
  { goos: 'windows', goarch: 'amd64', os: 'win32', cpu: 'x64', bin: 'netivri.exe', libc: null, pkg: 'netivri-w32-x64' },
  { goos: 'windows', goarch: 'arm64', os: 'win32', cpu: 'arm64', bin: 'netivri.exe', libc: null, pkg: 'netivri-w32-arm64' },
];

const REPOSITORY = { type: 'git', url: 'git+https://github.com/PHPCraftdream/netivri.git' };
const HOMEPAGE = 'https://github.com/PHPCraftdream/netivri#readme';
const BUGS = { url: 'https://github.com/PHPCraftdream/netivri/issues' };
const DESCRIPTION = 'Local SOCKS5/SOCKS4/HTTP proxy service with provider subscriptions and a per-account exit country, built on Mihomo';
const KEYWORDS = ['proxy', 'socks5', 'socks4', 'http-proxy', 'mihomo', 'clash', 'subscription', 'vless', 'cli'];

function fail(message) {
  throw new Error(message);
}

// Maps a directory name (or basename) to its platform, or null.
function archivePlatform(dirPath) {
  const m = path.basename(dirPath).match(/^netivri_.+_(linux|darwin|windows)_(amd64|arm64)$/);
  if (!m) return null;
  return PLATFORMS.find((p) => p.goos === m[1] && p.goarch === m[2]) || null;
}

// Detects the layout: one extracted archive directory directly (its
// basename names a platform), or a directory containing such directories.
// Returns [{ platform, dir }]; the main package's optionalDependencies
// always list all six platform packages regardless of how many are found.
function selectArchives(archiveDir) {
  const direct = archivePlatform(archiveDir);
  if (direct) {
    return [{ platform: direct, dir: archiveDir }];
  }
  let entries;
  try {
    entries = fs.readdirSync(archiveDir, { withFileTypes: true });
  } catch (err) {
    return fail(`cannot read archive dir ${archiveDir}: ${err.message}`);
  }
  const found = new Map();
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const platform = archivePlatform(entry.name);
    if (!platform) continue;
    if (found.has(platform.pkg)) {
      return fail(`ambiguous archive directories for ${platform.pkg} under ${archiveDir}`);
    }
    found.set(platform.pkg, { platform, dir: path.join(archiveDir, entry.name) });
  }
  if (found.size === 0) {
    return fail(`no extracted netivri_<version>_<os>_<arch> directories in ${archiveDir}`);
  }
  return [...found.values()];
}

function copyFile(src, dest, mode) {
  fs.copyFileSync(src, dest);
  if (mode != null) {
    fs.chmodSync(dest, mode);
  }
}

function copyDir(src, dest) {
  fs.mkdirSync(dest, { recursive: true });
  for (const entry of fs.readdirSync(src, { withFileTypes: true })) {
    const s = path.join(src, entry.name);
    const d = path.join(dest, entry.name);
    if (entry.isDirectory()) {
      copyDir(s, d);
    } else if (entry.isFile()) {
      fs.copyFileSync(s, d);
    }
  }
}

function requireFile(dir, name, what) {
  const p = path.join(dir, name);
  if (!fs.existsSync(p)) {
    fail(`${what} ${name} missing in ${dir}`);
  }
  return p;
}

function writeJson(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);
}

function buildPackages(options) {
  const version = options.version;
  const archiveDir = options.archiveDir;
  const out = options.out;
  if (!SEMVER_RE.test(version)) {
    fail(`invalid version "${version}": expected plain semver X.Y.Z (optional -prerelease)`);
  }
  if (!archiveDir || !fs.existsSync(archiveDir) || !fs.statSync(archiveDir).isDirectory()) {
    fail(`archive dir does not exist or is not a directory: ${archiveDir}`);
  }

  const archives = selectArchives(archiveDir);
  const packages = [];

  // Main package: launcher + README + licenses, version substituted. The
  // launcher is copied byte-for-byte and kept executable so npm's bin shim
  // (and the shebang) work on POSIX.
  const mainDir = path.join(out, 'netivri');
  const mainBin = path.join(mainDir, 'bin');
  fs.mkdirSync(mainBin, { recursive: true });
  copyFile(path.join(__dirname, '..', 'netivri', 'bin', 'netivri.js'), path.join(mainBin, 'netivri.js'), 0o755);
  copyFile(path.join(__dirname, '..', 'netivri', 'README.md'), path.join(mainDir, 'README.md'));
  const mainTpl = path.join(archives[0].dir, 'THIRD_PARTY_LICENSES');
  if (!fs.existsSync(mainTpl) || !fs.statSync(mainTpl).isDirectory()) {
    fail(`THIRD_PARTY_LICENSES/ missing in ${archives[0].dir}`);
  }
  const licenseSrc = requireFile(archives[0].dir, 'LICENSE', `LICENSE for ${archives[0].platform.goos}/${archives[0].platform.goarch}`);
  copyFile(licenseSrc, path.join(mainDir, 'LICENSE'));
  copyDir(path.join(archives[0].dir, 'THIRD_PARTY_LICENSES'), path.join(mainDir, 'THIRD_PARTY_LICENSES'));
  const optionalDeps = {};
  for (const platform of PLATFORMS) {
    optionalDeps[platform.pkg] = version;
  }
  writeJson(path.join(mainDir, 'package.json'), {
    name: 'netivri',
    version,
    description: DESCRIPTION,
    license: 'GPL-3.0-only',
    bin: { netivri: 'bin/netivri.js' },
    engines: { node: '>=18' },
    files: ['bin/', 'README.md', 'LICENSE', 'THIRD_PARTY_LICENSES/'],
    repository: REPOSITORY,
    homepage: HOMEPAGE,
    bugs: BUGS,
    keywords: KEYWORDS,
    os: ['linux', 'darwin', 'win32'],
    cpu: ['x64', 'arm64'],
    dependencies: {},
    optionalDependencies: optionalDeps,
  });
  packages.push({ name: 'netivri', path: mainDir });

  // One platform package per archive found.
  for (const { platform, dir } of archives) {
    const binSrc = requireFile(dir, platform.bin, `binary for ${platform.goos}/${platform.goarch}`);
    const pkgLicenseSrc = requireFile(dir, 'LICENSE', `LICENSE for ${platform.goos}/${platform.goarch}`);
    const tplDir = path.join(dir, 'THIRD_PARTY_LICENSES');
    if (!fs.existsSync(tplDir) || !fs.statSync(tplDir).isDirectory()) {
      fail(`THIRD_PARTY_LICENSES/ missing in ${dir}`);
    }

    const pkgDir = path.join(out, platform.pkg);
    const pkgBin = path.join(pkgDir, 'bin');
    fs.mkdirSync(pkgBin, { recursive: true });
    copyFile(binSrc, path.join(pkgBin, platform.bin), 0o755);
    copyFile(pkgLicenseSrc, path.join(pkgDir, 'LICENSE'));
    copyDir(tplDir, path.join(pkgDir, 'THIRD_PARTY_LICENSES'));

    const manifest = {
      name: platform.pkg,
      version,
      description: `netivri binary for ${platform.os} ${platform.cpu}${platform.libc ? ' (glibc)' : ''}`,
      license: 'GPL-3.0-only',
      os: [platform.os],
      cpu: [platform.cpu],
      files: ['bin/', 'LICENSE', 'THIRD_PARTY_LICENSES/'],
      engines: { node: '>=18' },
      repository: REPOSITORY,
      homepage: HOMEPAGE,
      bugs: BUGS,
    };
    if (platform.libc) {
      manifest.libc = platform.libc;
    }
    writeJson(path.join(pkgDir, 'package.json'), manifest);
    packages.push({ name: platform.pkg, path: pkgDir });
  }

  return { ok: true, version, packages };
}

// Command line uses kebab-case flags; values land in camelCase keys, the
// same names buildPackages() reads.
function parseArgs(argv) {
  const FLAGS = { '--version': 'version', '--archive-dir': 'archiveDir', '--out': 'out' };
  const parsed = {};
  for (let i = 0; i < argv.length; i++) {
    const key = FLAGS[argv[i]];
    if (!key) {
      fail(`unknown argument ${argv[i]}`);
    }
    const value = argv[i + 1];
    if (value == null || value.startsWith('--')) {
      fail(`missing value for ${argv[i]}`);
    }
    parsed[key] = value;
    i++;
  }
  for (const flag of Object.keys(FLAGS)) {
    if (!parsed[FLAGS[flag]]) {
      fail(`missing required argument ${flag}`);
    }
  }
  return parsed;
}

if (require.main === module) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const summary = buildPackages(args);
    console.log(JSON.stringify(summary));
  } catch (err) {
    console.error(JSON.stringify({ ok: false, error: { message: err.message } }));
    process.exitCode = 1;
  }
}

module.exports = { buildPackages, parseArgs, PLATFORMS };
