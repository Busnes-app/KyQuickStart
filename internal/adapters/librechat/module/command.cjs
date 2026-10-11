#!/usr/bin/env node
'use strict';
const path = require('node:path');
const fs = require('node:fs');
const { createRequire } = require('node:module');
const account = require('./account.cjs');

async function main() {
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === '--capabilities') {
    process.stdout.write(JSON.stringify({ contract: account.contract, module_version: account.version,
      features: ['account-inspection'] }) + '\n');
    return;
  }
  if (args.length !== 2 || args[0] !== '--file') { throw new Error('invalid arguments'); }
  const root = process.env.KYQ_LIBRECHAT_APP_ROOT || '/app';
  if (!path.isAbsolute(root) || path.normalize(root) !== root || fs.realpathSync(root) !== root) {
    throw new Error('invalid root');
  }
  const metadata = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
  if (metadata.name !== 'LibreChat' || metadata.version !== 'v0.8.8') {
    throw new Error('unsupported native release');
  }
  const request = account.readRequest(args[1], [path.join(root, 'client'), path.join(root, 'public'),
    path.join(root, 'uploads'), path.join(root, 'images')]);
  // Product dependencies resolve from the installed application, not the workstation.
  const requireNative = createRequire(path.join(root, 'package.json'));
  requireNative('dotenv').config({ path: path.join(root, '.env'), quiet: true });
  if (!process.env.MONGO_URI || process.env.OPENID_ISSUER !== request.issuer ||
      ![undefined, '', 'false'].includes(process.env.OPENID_REUSE_TOKENS)) {
    throw new Error('unsupported native configuration');
  }
  const mongoose = requireNative('mongoose');
  mongoose.set('autoIndex', false);
  mongoose.set('autoCreate', false);
  try {
    await mongoose.connect(process.env.MONGO_URI, { autoIndex: false, autoCreate: false,
      bufferCommands: false, maxPoolSize: 1, serverSelectionTimeoutMS: 10000 });
    // Register the same native schemas/methods as LibreChat. Avoid the broad API
    // model loader: it initializes cache stores that do not belong to inspection.
    const { createModels, createMethods, runAsSystem } = requireNative('@librechat/data-schemas');
    createModels(mongoose);
    const native = createMethods(mongoose);
    const report = await account.inspect(request, { getUserById: native.getUserById, runAsSystem },
      process.env.OPENID_ISSUER);
    process.stdout.write(JSON.stringify(report) + '\n');
  } finally { await mongoose.disconnect(); }
}
main().catch(() => { process.stderr.write('LibreChat native inspection refused\n'); process.exitCode = 1; });
