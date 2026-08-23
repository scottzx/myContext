#!/usr/bin/env node
'use strict';

// Fallback launcher. On POSIX the postinstall step replaces this with a direct
// symlink to the native binary, because paying Node's startup cost on every
// invocation matters on iSH, where the whole point is short-lived commands.
const { spawnSync } = require('node:child_process');
const { resolveBinary } = require('./resolve.js');

let binary;
try {
  binary = resolveBinary();
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exit(8);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' });

if (result.error) {
  process.stderr.write(`failed to run mycontext: ${result.error.message}\n`);
  process.exit(8);
}
// Preserve the CLI's own exit code: callers branch on it.
process.exit(result.status === null ? 8 : result.status);
