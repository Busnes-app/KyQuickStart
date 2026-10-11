'use strict';
// Native source hooks use the application's existing connection and models.
const path = require('node:path');
const { createRequire } = require('node:module');
const nativeRequire = createRequire(path.join(process.env.KYQ_LIBRECHAT_APP_ROOT || '/app', 'package.json'));
const mongoose = nativeRequire('mongoose');
const { runAsSystem } = nativeRequire('@librechat/data-schemas');
const core = require('./revocation.cjs');

function native() {
  if (!mongoose.models.User) { throw new Error('Account access refused'); }
  return { mongoose, User: mongoose.models.User, runAsSystem };
}

function denied() {
  const error = new Error('Account access refused');
  error.status = 403;
  error.statusCode = 403;
  return error;
}

async function assertAccountActive(id) {
  if (id instanceof mongoose.Types.ObjectId) { id = id.toHexString(); }
  try { await core.assertAccountActive(id, native()); } catch { throw denied(); }
}

async function assertSubjectActive(issuer, subject) {
  try { await core.assertSubjectActive(issuer, subject, native()); } catch { throw denied(); }
}

async function isAccountActive(id) {
  try { await assertAccountActive(id); return true; } catch { return false; }
}

async function findActiveUser(findUser, ...args) {
  const user = await findUser(...args);
  if (user && !(await isAccountActive(user._id.toString()))) { return null; }
  return user;
}

module.exports = { assertAccountActive, assertSubjectActive, isAccountActive, findActiveUser };
