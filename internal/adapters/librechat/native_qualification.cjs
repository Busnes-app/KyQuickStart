// Executes upstream modules unchanged; only storage/dependency boundaries are substituted.
// This is a prerequisite probe, not a full HTTP/MongoDB or cryptographic qualification.
'use strict';
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const root = process.argv[2];
assert(root && process.argv.length === 3, 'usage: native_qualification.cjs <pinned checkout>');
const commit = 'e8f3be08623663d4ad7f7241e693c94469b63bb0';
assert.equal(execFileSync('git', ['-C', root, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), commit);
const sources = {
  'api/server/middleware/checkBan.js': '890c3772847685f671fce7031424cb3a0f47b7c3332767c22f3cae92b06a2890',
  'api/cache/banViolation.js': '061ab4b90fa53ebbd2b351e9bf161d4aec219cfb9a909593270ed1c14629da6f',
  'api/strategies/jwtStrategy.js': '12bd0aa23bf0882e4b7c7f137fa5bce202a87d35a475e7c8b69235578ed79f3b',
  'api/server/routes/admin/users.js': 'f4946ca87292f719540e55c33483eccf46f279890326824c07bdad6bcf34660d',
};
let now = 100000;
const env = { BAN_VIOLATIONS: 'true', BAN_INTERVAL: '20', USE_REDIS: 'false', JWT_SECRET: 'fixture-only' };
const logger = { info() {}, warn() {}, error() {} };
const violations = { BAN: 'ban', CONCURRENT: 'concurrent' };
const api = {
  isEnabled: value => value === 'true',
  math: (value, fallback) => Number(value) || fallback,
  removePorts: req => req.ip,
  getBanIp: req => req.ip,
};

function load(file, dependencies) {
  const source = fs.readFileSync(path.join(root, file));
  assert.equal(crypto.createHash('sha256').update(source).digest('hex'), sources[file], 'native source changed');
  const module = { exports: {} };
  const context = vm.createContext({
    module, exports: module.exports, process: { env }, URL,
    Date: class extends Date { static now() { return now; } },
    require(name) {
      assert(Object.hasOwn(dependencies, name), `unexpected native dependency: ${name}`);
      return dependencies[name];
    },
  });
  new vm.Script(source.toString(), { filename: file }).runInContext(context, { timeout: 1000 });
  return module.exports;
}

class Store {
  constructor(options = {}) { this.opts = options; this.data = new Map(); }
  async get(key) {
    const entry = this.data.get(key);
    if (!entry || (entry.until && entry.until <= now)) { this.data.delete(key); return undefined; }
    return entry.value;
  }
  async set(key, value, ttl = this.opts.ttl) {
    this.data.set(key, { value, until: ttl > 0 ? now + ttl : 0 });
  }
  async delete(key) { return this.data.delete(key); }
}

const user = { _id: '0123456789abcdef01234567', role: 'USER', provider: 'openid',
  openidId: 'exact-subject', openidIssuer: 'https://identity.example.test' };
const sessions = new Set(['retained-refresh-session']);
const bans = new Store({ ttl: 60000 });
const models = {
  async deleteAllUserSessions({ userId }) { assert.equal(userId, user._id); sessions.clear(); },
  async findUser() { return user; },
  async getUserById(id) { return id === user._id ? { ...user } : null; },
  async updateUser() { throw new Error('unexpected native user mutation'); },
};
const ban = load('api/cache/banViolation.js', {
  '@librechat/data-schemas': { logger },
  'librechat-data-provider': { ViolationTypes: violations },
  '@librechat/api': api, '~/models': models, './getLogStores': () => bans,
});
const checkBan = load('api/server/middleware/checkBan.js', {
  keyv: { Keyv: Store }, 'ua-parser-js': () => ({ browser: {} }),
  '@librechat/data-schemas': { logger },
  'librechat-data-provider': { ViolationTypes: violations, ErrorTypes: {} },
  '@librechat/api': api, '~/cache': { getLogStores: () => bans }, '~/models': models,
  './oauthNavigation': { isOAuthNavigation: () => false },
  './denyRequest': () => { throw new Error('unexpected browser response'); },
});
const jwt = load('api/strategies/jwtStrategy.js', {
  '@librechat/api': { AGENT_TRIGGER_SCOPE: 'trigger' },
  '@librechat/data-schemas': { logger, runAsSystem: callback => callback() },
  'librechat-data-provider': { SystemRoles: { USER: 'USER' } },
  'passport-jwt': { Strategy: class { constructor(options, verify) { this.verify = verify; } },
    ExtractJwt: { fromAuthHeaderAsBearerToken: () => () => 'fixture-token' } },
  '~/models': models,
})();

function response() {
  return { code: 0, cookies: [], clearCookie(name) { this.cookies.push(name); },
    status(code) { this.code = code; return this; }, json() { return this; } };
}

async function main() {
  const res = response();
  const violation = { type: violations.CONCURRENT, user_id: user._id,
    violation_count: 20, prev_count: 0, duration: 1000 };
  await ban({}, res, violation);
  assert.equal(sessions.size, 0);
  assert.equal(violation.ban, true);
  assert(res.cookies.includes('refreshToken'));
  let allowed = false;
  await checkBan({ user: { id: user._id }, headers: {} }, res, () => { allowed = true; });
  assert.equal(allowed, false);
  assert.equal(res.code, 403);

  // TTL expiry changes only storage visibility/time, not the native user or bearer payload.
  now += 1001;
  const jwtResult = await new Promise((resolve, reject) => {
    jwt.verify({}, { id: user._id }, (error, account) => error ? reject(error) : resolve(account));
  });
  assert.equal(jwtResult.id, user._id);
  assert.equal(jwtResult.openidId, user.openidId);
  await checkBan({ user: jwtResult, headers: {} }, response(), () => { allowed = true; });
  assert.equal(allowed, true);
  assert.equal(user.provider, 'openid');
  console.log('PASS: native ban deletes refresh sessions, denies during TTL, then admits retained-user JWT strategy result after expiry.');

  // Disabling BAN_VIOLATIONS bypasses even an otherwise live ban.
  now = 200000;
  await ban({}, response(), { ...violation, prev_count: 0 });
  env.BAN_VIOLATIONS = 'false';
  let bypass = false;
  await checkBan({ user: { id: user._id }, headers: {} }, response(), () => { bypass = true; });
  assert.equal(bypass, true);
  console.log('PASS: native ban enforcement is optional; this is not permanent retained-account revocation.');

  const registered = [];
  const router = {
    use() {},
    get(route) { registered.push(['GET', route]); },
    delete(route) { registered.push(['DELETE', route]); },
    post(route) { registered.push(['POST', route]); },
    patch(route) { registered.push(['PATCH', route]); },
    put(route) { registered.push(['PUT', route]); },
  };
  const noop = () => {};
  load('api/server/routes/admin/users.js', {
    express: { Router: () => router }, mongoose: {},
    '@librechat/api': { createAdminUsersHandlers: () => ({ listUsers: noop, searchUsers: noop, deleteUser: noop }) },
    '@librechat/data-schemas': { SystemCapabilities: { ACCESS_ADMIN: 'admin', READ_USERS: 'read' } },
    '~/server/middleware/roles/capabilities': { requireCapability: () => noop },
    '~/server/middleware': { requireJwtAuth: noop }, '~/models': {},
    '~/server/services/Agents/triggers': {}, '~/server/services/Config': {},
  });
  assert.deepEqual(registered, [['GET', '/'], ['GET', '/search']]);
  console.log('PASS: native admin user router exposes list/search only; no disable or token-revocation route.');
  console.log(`QUALIFIED GAP: LibreChat v0.8.8 ${commit}; scoped ownership inspection and persistent retained revocation must be built before admission.`);
}
main().catch(error => { console.error(error); process.exitCode = 1; });
