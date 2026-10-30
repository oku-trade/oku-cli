#!/usr/bin/env node
'use strict';
// Thin launcher: execs the platform binary from an optional dependency.
// No shell, no downloads. Preserves argv, stdio, signals and exit status.
const { spawn } = require('node:child_process');
const path = require('node:path');

const PLATFORMS = {
  'linux-x64': '@gfxlabs/oku-cli-linux-x64',
  'linux-arm64': '@gfxlabs/oku-cli-linux-arm64',
  'darwin-x64': '@gfxlabs/oku-cli-darwin-x64',
  'darwin-arm64': '@gfxlabs/oku-cli-darwin-arm64',
};

function resolveBinary() {
  if (process.env.OKU_BINARY_PATH) return process.env.OKU_BINARY_PATH;
  const key = `${process.platform}-${process.arch}`;
  const pkg = PLATFORMS[key];
  if (!pkg) {
    fail(`oku: unsupported platform ${key}. Supported: ${Object.keys(PLATFORMS).join(', ')}`);
  }
  try {
    return path.join(path.dirname(require.resolve(`${pkg}/package.json`)), 'bin', 'oku');
  } catch {
    fail(`oku: platform package ${pkg} is not installed. It is an optionalDependency; ` +
      'reinstall without --omit=optional / --no-optional, or set OKU_BINARY_PATH.');
  }
}

function fail(msg) {
  process.stderr.write(msg + '\n');
  process.exit(1);
}

const child = spawn(resolveBinary(), process.argv.slice(2), { stdio: 'inherit', shell: false });
const forwarded = ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGQUIT', 'SIGUSR1', 'SIGUSR2', 'SIGWINCH'];
const handlers = {};
for (const sig of forwarded) {
  handlers[sig] = () => { try { child.kill(sig); } catch { /* child gone */ } };
  process.on(sig, handlers[sig]);
}
child.on('error', (err) => fail(`oku: failed to start binary: ${err.message}`));
child.on('exit', (code, signal) => {
  for (const sig of forwarded) process.removeListener(sig, handlers[sig]);
  if (signal) {
    process.kill(process.pid, signal); // re-raise so the parent sees the same signal
    return;
  }
  process.exit(code ?? 1);
});
