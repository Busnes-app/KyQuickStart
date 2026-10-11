'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const writers = require('./writers.cjs');

// Captures the hooks register() installs on a User-shaped schema. Real Mongoose
// routing is qualified against MongoDB in revocation.native.cjs.
function userHooks() {
  const hooks = new Map();
  const paths = new Set(['openidId', 'openidIssuer', 'provider']);
  const schema = { path: name => paths.has(name) || undefined, add() {},
    pre(method, options, fn) { hooks.set(method, fn || options); } };
  writers.register({ models: {}, Schema: { Types: { Mixed: Object } }, plugin: fn => fn(schema) });
  return hooks;
}
const run = (hook, filter) => {
  const query = { filter, getQuery() { return this.filter; }, setQuery(next) { this.filter = next; } };
  hook.call(query);
  return query.filter;
};

test('every Mongoose 8 query write is fenced; deletes match the deleteOne restriction', () => {
  const hooks = userHooks();
  // Mongoose 8.24 lib/constants.js queryOperations minus reads; findOneAndRemove no longer exists.
  for (const method of ['updateOne', 'updateMany', 'findOneAndUpdate', 'replaceOne',
    'findOneAndReplace', 'deleteOne', 'deleteMany', 'findOneAndDelete']) {
    assert(hooks.has(method), method);
  }
  assert(!hooks.has('findOneAndRemove'));
  const filter = { _id: '0123456789abcdef01234567' };
  const fenced = run(hooks.get('deleteOne'), filter);
  assert.deepEqual(run(hooks.get('findOneAndDelete'), filter), fenced);
  assert.deepEqual(run(hooks.get('deleteMany'), filter), fenced);
  const clauses = JSON.stringify(fenced);
  for (const field of ['kyquickstartRevocation', 'kyquickstartCredentialWrites', 'kyquickstartCredentialRecovery']) {
    assert(clauses.includes(field), field);
  }
});
