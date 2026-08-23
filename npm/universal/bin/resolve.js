'use strict';

// Maps the running platform to the package holding its binary. Platform
// packages are exact-versioned optionalDependencies of the universal package,
// so npm installs only the one matching the machine (via os/cpu fields).
const { existsSync, realpathSync } = require('node:fs');
const { join, dirname, parse } = require('node:path');

const PACKAGE_BY_TARGET = {
  'darwin-arm64': '@1agents/mycontext-darwin-arm64',
  'darwin-x64': '@1agents/mycontext-darwin-x64',
  'linux-ia32': '@1agents/mycontext-linux-ia32',
  'linux-x64': '@1agents/mycontext-linux-x64',
  'linux-arm64': '@1agents/mycontext-linux-arm64',
};

function target() {
  return `${process.platform}-${process.arch}`;
}

function binaryName() {
  return process.platform === 'win32' ? 'mycontext.exe' : 'mycontext';
}

// searchRoots returns every directory worth walking up from.
//
// require.resolve alone is not enough: it resolves relative to this file's
// *real* path, so when the package is symlinked into node_modules — npm link,
// pnpm's default layout, or `npm install ../local/dir` — the sibling platform
// package is invisible. Starting from the invoked path as well as the real
// path covers both layouts.
function searchRoots() {
  const roots = [__dirname];
  const invoked = process.argv[1];
  if (invoked) {
    roots.push(dirname(invoked));
    try {
      roots.push(dirname(realpathSync(invoked)));
    } catch {
      /* the launcher may not be a real file; ignore */
    }
  }
  roots.push(process.cwd());
  return [...new Set(roots)];
}

// findInNodeModules walks up from each root looking for the platform package.
function findInNodeModules(pkg) {
  const relative = join('node_modules', ...pkg.split('/'), 'bin', binaryName());
  for (const root of searchRoots()) {
    let dir = root;
    const { root: filesystemRoot } = parse(dir);
    while (true) {
      const candidate = join(dir, relative);
      if (existsSync(candidate)) return candidate;
      if (dir === filesystemRoot) break;
      const parent = dirname(dir);
      if (parent === dir) break;
      dir = parent;
    }
  }
  return null;
}

// resolveBinary returns the absolute path to the native binary, or throws a
// message saying exactly what is missing and how to fix it.
function resolveBinary() {
  const key = target();
  const pkg = PACKAGE_BY_TARGET[key];
  if (!pkg) {
    const supported = Object.keys(PACKAGE_BY_TARGET).join(', ');
    throw new Error(
      `mycontext has no prebuilt binary for ${key}.\nSupported platforms: ${supported}`
    );
  }

  // Fast path: the standard flat or nested node_modules layout.
  try {
    const packageJsonPath = require.resolve(`${pkg}/package.json`);
    const binary = join(dirname(packageJsonPath), 'bin', binaryName());
    if (existsSync(binary)) return binary;
  } catch {
    /* fall through to the directory walk */
  }

  const found = findInNodeModules(pkg);
  if (found) return found;

  let version = 'latest';
  try {
    version = require('../package.json').version;
  } catch {
    /* keep the generic hint */
  }
  throw new Error(
    `The platform package ${pkg} is not installed.\n` +
      `This usually means npm skipped optional dependencies.\n` +
      `Fix: npm install ${pkg}@${version}`
  );
}

module.exports = { resolveBinary, target, binaryName, PACKAGE_BY_TARGET };
