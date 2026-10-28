#!/usr/bin/env node
'use strict';

// netivri launcher: resolves the platform binary shipped as an npm
// optionalDependency and execs it. No dependencies, no network access.

const { spawn } = require('child_process');
const path = require('path');

const ERROR_CODE = 'INSTALL_BROKEN';
const FORWARDED_SIGNALS = ['SIGINT', 'SIGTERM', 'SIGHUP'];
const SIGNAL_NUMBERS = { SIGHUP: 1, SIGINT: 2, SIGTERM: 15 };

const PACKAGE_BY_TARGET = {
  'linux-x64': '@netivri/proxy-linux-x64',
  'linux-arm64': '@netivri/proxy-linux-arm64',
  'darwin-x64': '@netivri/proxy-darwin-x64',
  'darwin-arm64': '@netivri/proxy-darwin-arm64',
  'win32-x64': '@netivri/proxy-w32-x64',
  'win32-arm64': '@netivri/proxy-w32-arm64',
};

// Pure mapping used by the launcher and the tests. Returns the platform
// package name, or null when the platform is unsupported. The Linux binary is
// a static Go build, so one package serves glibc and musl (Alpine) alike.
function platformPackage(platform, arch) {
  return PACKAGE_BY_TARGET[`${platform}-${arch}`] || null;
}

function errorEnvelope(message, hint) {
  return {
    schema_version: 1,
    ok: false,
    error: { code: ERROR_CODE, message, hint },
  };
}

const HINT_MISSING_PKG = 'reinstall the package: npm i -g @netivri/proxy (do not use --no-optional or --omit=optional)';

function unsupportedMessage(platform, arch) {
  return `unsupported platform ${platform}-${arch}`;
}

// Resolves the platform binary. `resolve` is injectable for tests (defaults
// to require.resolve). Returns { bin } or { error: { message, hint } }.
function resolveBinary(platform, arch, resolve) {
  const pkg = platformPackage(platform, arch);
  if (!pkg) {
    return {
      error: {
        message: unsupportedMessage(platform, arch),
        hint: 'check the supported platforms in the netivri README',
      },
    };
  }
  let pkgJsonPath;
  try {
    pkgJsonPath = resolve(`${pkg}/package.json`);
  } catch (_err) {
    return {
      error: {
        message: `platform package ${pkg} is not installed`,
        hint: HINT_MISSING_PKG,
      },
    };
  }
  const binName = platform === 'win32' ? 'netivri.exe' : 'netivri';
  return { bin: path.join(path.dirname(pkgJsonPath), 'bin', binName) };
}

// Spawns the binary, forwards SIGINT/SIGTERM/SIGHUP to it and resolves with
// the exit code (128 + signal number when the child died from a signal).
// stdout/stderr are inherited by default so the child's JSON output is
// never swallowed. The spawned child is exposed as promise.child.
function run(binPath, args, options) {
  const spawnOptions = Object.assign(
    { stdio: 'inherit', windowsHide: true },
    options || {}
  );
  const child = spawn(binPath, args, spawnOptions);
  const forward = (signal) => {
    child.kill(signal);
  };
  for (const sig of FORWARDED_SIGNALS) {
    process.on(sig, forward);
  }
  const cleanup = () => {
    for (const sig of FORWARDED_SIGNALS) {
      process.off(sig, forward);
    }
  };
  const promise = new Promise((resolveExit) => {
    child.on('error', (_err) => {
      cleanup();
      printError(errorEnvelope(
        `cannot start ${binPath}: the netivri installation is broken`,
        HINT_MISSING_PKG
      ));
      resolveExit(1);
    });
    // 'close' (not 'exit'): resolve only after the child's stdio streams
    // have closed, so piped output is complete when the promise settles.
    child.on('close', (code, signal) => {
      cleanup();
      if (signal != null) {
        resolveExit(128 + (SIGNAL_NUMBERS[signal] || 1));
        return;
      }
      resolveExit(code == null ? 1 : code);
    });
  });
  promise.child = child;
  return promise;
}

function printError(envelope) {
  console.error(JSON.stringify(envelope));
}

async function main(argv) {
  const args = argv || process.argv.slice(2);
  const resolved = resolveBinary(process.platform, process.arch, require.resolve);
  if (resolved.error) {
    printError(errorEnvelope(resolved.error.message, resolved.error.hint));
    return 1;
  }
  return run(resolved.bin, args);
}

if (require.main === module) {
  main().then((code) => {
    process.exitCode = code;
  });
}

module.exports = {
  ERROR_CODE,
  errorEnvelope,
  main,
  platformPackage,
  printError,
  resolveBinary,
  run,
};
