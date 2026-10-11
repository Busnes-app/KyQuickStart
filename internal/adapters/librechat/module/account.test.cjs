'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { execFileSync } = require('node:child_process');
const account = require('./account.cjs');
const request = { contract: account.contract, user_id: '0123456789abcdef01234567',
  issuer: 'https://identity.example.test', subject: 'subject-雪' };

test('exact native provider/source/subject/ID and bounded role inspection without writes', async () => {
  const user = { _id: request.user_id, provider: 'openid', openidId: request.subject,
    openidIssuer: request.issuer, role: 'USER', email: 'private@example.test', name: 'private' };
  let reads = 0;
  const native = { runAsSystem: fn => fn(), getUserById: async (id, fields) => {
    reads++; assert.equal(id, request.user_id); assert.equal(fields, '_id provider openidId openidIssuer role');
    return user;
  } };
  const before = JSON.stringify(user);
  const report = await account.inspect(request, native, request.issuer);
  assert.equal(report.role, 'USER');
  assert.equal(report.user_id, request.user_id);
  assert(!JSON.stringify(report).includes('private'));
  assert(!JSON.stringify(report).includes(request.subject));
  assert.equal(JSON.stringify(user), before);
  user.role = 'ADMIN';
  assert.equal((await account.inspect(request, native, request.issuer)).role, 'ADMIN');
  for (const [key, value] of [['provider', 'local'], ['openidId', 'other'],
    ['openidIssuer', 'https://foreign.test'], ['_id', '111111111111111111111111'], ['role', 'CUSTOM']]) {
    const old = user[key]; user[key] = value;
    await assert.rejects(account.inspect(request, native, request.issuer));
    user[key] = old;
  }
  await assert.rejects(account.inspect(request, native, 'https://foreign.test'));
  await assert.rejects(account.inspect(request, { runAsSystem: fn => fn(), getUserById: async () => null }, request.issuer));
  assert.equal(reads, 7);
});

test('private request boundaries, byte bounds, unknown input and nonmutation', t => {
  const dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'kyq-librechat-inspect.'))); // macOS /var symlink
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  fs.chmodSync(dir, 0o700);
  const file = path.join(dir, 'request.json');
  const put = value => fs.writeFileSync(file, value, { mode: 0o600 });
  put(JSON.stringify(request));
  const before = fs.readFileSync(file);
  assert.deepEqual(account.readRequest(file, []), request);
  assert.deepEqual(fs.readFileSync(file), before);
  assert.throws(() => account.readRequest(file, [dir]));
  fs.chmodSync(file, 0o644);
  assert.throws(() => account.readRequest(file, [])); fs.chmodSync(file, 0o600);
  const link = path.join(dir, 'link'); fs.symlinkSync(file, link);
  assert.throws(() => account.readRequest(link, []));
  const fifo = path.join(dir, 'fifo'); execFileSync('mkfifo', ['-m', '600', fifo]);
  assert.throws(() => account.readRequest(fifo, []));
  fs.chmodSync(dir, 0o777); assert.throws(() => account.readRequest(file, [])); fs.chmodSync(dir, 0o700);
  for (const change of [{ user_id: 'EMAIL' }, { issuer: 'http://identity.test' },
    { issuer: 'https://user:secret@identity.test' }, { issuer: 'https://identity.test?' },
    { issuer: 'https://identity.test#' }, { issuer: 'https://identity.test\\other' },
    { subject: '' }, { subject: 'x'.repeat(256) }, { subject: '\ud800' }, { unknown: true },
    { contract: 'ky.librechat-offboard/1' }]) {
    put(JSON.stringify({ ...request, ...change })); assert.throws(() => account.readRequest(file, []));
  }
  for (const data of [Buffer.from([0xff]), 'x'.repeat(4097), '{} trailing', '[]']) {
    put(data); assert.throws(() => account.readRequest(file, []));
  }
});
