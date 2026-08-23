#!/usr/bin/env node
'use strict';

// Replaces the Node launcher with the native binary so that every `mycontext`
// invocation is a single exec.
//
// npm's .bin/mycontext points at bin/mycontext.js, so the native binary has to
// take over *that* path; creating a sibling file would leave the Node hop in
// place.
//
// This step downloads nothing: the binary already arrived inside the platform
// package's npm tarball. Any failure leaves the working Node launcher.
const { existsSync, unlinkSync, symlinkSync, copyFileSync, chmodSync, statSync } = require('node:fs');
const { join } = require('node:path');

function materialise() {
  if (process.platform === 'win32') return; // .cmd shims invoke node directly

  let resolveBinary;
  try {
    ({ resolveBinary } = require('./resolve.js'));
  } catch {
    return;
  }

  let binary;
  try {
    binary = resolveBinary();
  } catch (error) {
    // Not fatal: the launcher reports this properly at run time.
    process.stderr.write(`mycontext: ${error.message}\n`);
    return;
  }

  const shim = join(__dirname, 'mycontext.js');
  const launcher = join(__dirname, 'launcher.js');
  if (!existsSync(launcher)) return; // no fallback to fall back to; leave as is

  try {
    chmodSync(binary, 0o755);
  } catch {
    /* the file may already be executable and not owned by us */
  }

  try {
    if (existsSync(shim) || isBrokenLink(shim)) unlinkSync(shim);
    symlinkSync(binary, shim);
  } catch {
    try {
      if (existsSync(shim)) unlinkSync(shim);
      copyFileSync(binary, shim);
      chmodSync(shim, 0o755);
    } catch (error) {
      process.stderr.write(
        `mycontext: keeping the Node launcher (${error.message}); ` +
          'startup will be slower but behaviour is identical\n'
      );
    }
  }
}

function isBrokenLink(path) {
  try {
    statSync(path);
    return false;
  } catch {
    return true;
  }
}

materialise();
