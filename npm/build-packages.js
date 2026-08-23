#!/usr/bin/env node
'use strict';

// Assembles the npm publish tree from binaries already built by `make release`.
// It never downloads anything: every executable comes from the local build
// output and ships inside its platform package's tarball.
//
//   node npm/build-packages.js [version]
//
// Output: npm/dist/<package>/ ready for `npm publish`.

const { mkdirSync, copyFileSync, writeFileSync, existsSync, rmSync, readFileSync } = require('node:fs');
const { join, dirname } = require('node:path');

const ROOT = join(__dirname, '..');
const BUILD_DIR = join(ROOT, 'build');
const DIST = join(__dirname, 'dist');

// os/cpu let npm skip packages that do not match the installing machine.
// iSH reports linux/ia32, which is why linux-ia32 is the required target.
const TARGETS = [
  { name: 'darwin-arm64', goos: 'darwin', goarch: 'arm64', os: 'darwin', cpu: 'arm64' },
  { name: 'darwin-x64', goos: 'darwin', goarch: 'amd64', os: 'darwin', cpu: 'x64' },
  { name: 'linux-ia32', goos: 'linux', goarch: '386', os: 'linux', cpu: 'ia32' },
  { name: 'linux-x64', goos: 'linux', goarch: 'amd64', os: 'linux', cpu: 'x64' },
  { name: 'linux-arm64', goos: 'linux', goarch: 'arm64', os: 'linux', cpu: 'arm64' },
];

const version = process.argv[2] || require('./universal/package.json').version;

function platformPackage(target) {
  const pkgName = `@1agents/mycontext-${target.name}`;
  const dir = join(DIST, `mycontext-${target.name}`);
  const source = join(BUILD_DIR, `mycontext-${target.goos}-${target.goarch}`);

  if (!existsSync(source)) {
    return { pkgName, skipped: true, reason: `missing ${source} (run: make release)` };
  }

  mkdirSync(join(dir, 'bin'), { recursive: true });
  copyFileSync(source, join(dir, 'bin', 'mycontext'));

  writeFileSync(
    join(dir, 'package.json'),
    JSON.stringify(
      {
        name: pkgName,
        version,
        description: `mycontext native binary for ${target.os}/${target.cpu}`,
        license: 'MIT',
        os: [target.os],
        cpu: [target.cpu],
        files: ['bin/'],
        preferUnplugged: true,
      },
      null,
      2
    ) + '\n'
  );
  return { pkgName, dir, skipped: false };
}

function universalPackage(results) {
  const dir = join(DIST, 'mycontext');
  mkdirSync(join(dir, 'bin'), { recursive: true });

  for (const file of ['mycontext.js', 'launcher.js', 'install.js', 'resolve.js']) {
    copyFileSync(join(__dirname, 'universal', 'bin', file), join(dir, 'bin', file));
  }

  const pkg = JSON.parse(readFileSync(join(__dirname, 'universal', 'package.json'), 'utf8'));
  pkg.version = version;
  // Exact versions only: a range could resolve a platform package that does
  // not exist yet, which is why platform packages publish first.
  pkg.optionalDependencies = Object.fromEntries(
    results.filter((r) => !r.skipped).map((r) => [r.pkgName, version])
  );
  writeFileSync(join(dir, 'package.json'), JSON.stringify(pkg, null, 2) + '\n');

  // The command catalog ships with the universal package so an agent can read
  // the contract without executing anything.
  const catalog = join(ROOT, 'schemas', 'catalog.json');
  if (existsSync(catalog)) {
    mkdirSync(join(dir, 'schemas'), { recursive: true });
    copyFileSync(catalog, join(dir, 'schemas', 'catalog.json'));
  }
  const readme = join(ROOT, 'README.md');
  if (existsSync(readme)) copyFileSync(readme, join(dir, 'README.md'));

  return dir;
}

function main() {
  rmSync(DIST, { recursive: true, force: true });
  mkdirSync(DIST, { recursive: true });

  const results = TARGETS.map(platformPackage);
  for (const r of results) {
    console.log(r.skipped ? `skip  ${r.pkgName}  (${r.reason})` : `ok    ${r.pkgName}`);
  }

  const built = results.filter((r) => !r.skipped);
  if (built.length === 0) {
    console.error('\nNo platform binaries found. Run `make release` first.');
    process.exit(1);
  }
  const universal = universalPackage(results);
  console.log(`ok    @1agents/mycontext  ->  ${universal}`);

  console.log('\nPublish platform packages FIRST, then the universal package:');
  for (const r of built) console.log(`  npm publish ${r.dir} --access public`);
  console.log(`  npm publish ${universal} --access public`);

  void dirname;
}

main();
