#!/usr/bin/env node
'use strict';

// This file is the target of npm's .bin/mycontext shim.
//
// After a successful postinstall it is REPLACED by a link to the native
// binary, so `mycontext ...` costs one exec instead of a Node startup plus an
// exec. That matters most on iSH, where Node startup under x86 emulation
// dominates the runtime of a short command.
//
// If postinstall could not do that, this Node launcher still works.
require('./launcher.js');
