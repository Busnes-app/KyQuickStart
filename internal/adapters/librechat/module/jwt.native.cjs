'use strict';
// Run the patched native Passport strategy with real cryptography and MongoDB.
// Only its application model alias is resolved here; methods remain native.
const fs = require('node:fs');
const Module = require('node:module');
const { createRequire } = Module;
const nativeRequire = createRequire('/app/package.json');
const jwt = nativeRequire('jsonwebtoken');
const runtime = require('./runtime.cjs');

module.exports = async function probe(native) {
  function load(relative, config) {
    const filename = '/app/' + relative;
    const loaded = new Module(filename, module);
    loaded.paths = Module._nodeModulePaths(require('node:path').dirname(filename));
    const originalRequire = loaded.require.bind(loaded);
    loaded.require = name => {
      if (name === '~/models') { return native; }
      if (name === '~/kyquickstart/runtime.cjs') { return runtime; }
      if (name === '~/kyquickstart/writers.cjs') { return require('./writers.cjs'); }
      if (name === '~/server/services/AuthService') { return auth; }
      // Provider refresh is outside this local-session fixture. Refuse those
      // boundaries instead of returning a fabricated provider response.
      const inactive = () => { throw new Error('Fixture provider refresh boundary'); };
      if (name === '~/server/services/GraphTokenService') { return { getGraphApiToken: inactive }; }
      if (name === '~/server/services/RefreshTokenBridge') { return { getRefreshTokenBridge: inactive }; }
      if (name === '~/server/services/OpenIDRefreshRecovery') {
        return { recoverOpenIDRefreshBridge: inactive, refreshOpenIDUser: inactive, sendOpenIDAuthResponse: inactive };
      }
      if (name === '~/server/services/OpenIDRefreshFlight') {
        return { assertOpenIDRefreshFlightDeliveryAvailable: inactive, assertOpenIDRefreshSessionGenerationAvailable: inactive,
          claimOpenIDRefreshFlightDelivery: inactive, releaseOpenIDRefreshFlightDelivery: inactive };
      }
      if (name === '~/strategies/validators') { return nativeRequire('/app/api/strategies/validators'); }
      // Unused configuration/email boundaries refuse if unexpectedly exercised.
      if (name === '~/server/services/Config' || name === '~/server/utils') {
        return { getAppConfig: () => { if (config) { return config; } throw new Error('Fixture configuration boundary'); },
          sendEmail: () => { throw new Error('Fixture email boundary'); } };
      }
      if (name === '~/server/services/PermissionService') {
        return { getEffectivePermissions: () => { throw new Error('Fixture agent permission boundary'); } };
      }
      if (name === '~/cache/getLogStores') {
        const stores = new Map();
        return key => { if (!stores.has(key)) { stores.set(key, new (nativeRequire('keyv').Keyv)()); } return stores.get(key); };
      }
      if (name === '~/server/services/Files/strategies') {
        return { getStrategyFunctions: () => { throw new Error('Fixture avatar boundary'); } };
      }
      if (name === '~/server/services/Files/images/avatar') {
        return { resizeAvatar: () => { throw new Error('Fixture avatar boundary'); } };
      }
      return originalRequire(name);
    };
    const overlay = '/overlay/' + relative;
    loaded._compile(fs.readFileSync(fs.existsSync(overlay) ? overlay : filename, 'utf8'), filename);
    return loaded.exports;
  }
  const strategy = load('api/strategies/jwtStrategy.js');
  if (load('api/db/models.js').User !== native.User) { throw new Error('Native model exports refused'); }
  const auth = load('api/server/services/AuthService.js');
  const share = load('api/server/middleware/optionalShareFileAuth.js');
  const image = load('api/server/middleware/validateImageRequest.js')(true);
  const passport = nativeRequire('passport');
  const agents = load('api/server/routes/agents/middleware.js', {
    endpoints: { agents: { remoteApi: { auth: { apiKey: { enabled: true }, oidc: { enabled: false } } } } },
  });
  process.env.JWT_SECRET = 'disposable-native-jwt-fixture-secret-never-deployed';
  process.env.JWT_REFRESH_SECRET = 'disposable-native-refresh-fixture-secret-never-deployed';
  passport.use('jwt', strategy());
  const requireJwtAuth = load('api/server/middleware/requireJwtAuth.js');
  const sign = id => jwt.sign({ id }, process.env.JWT_SECRET, { expiresIn: '5m' });
  const authenticate = token => new Promise((resolve, reject) => {
    const instance = strategy();
    instance.success = user => resolve(user.id);
    instance.fail = () => resolve(null);
    instance.error = reject;
    instance.authenticate({ method: 'GET', url: '/api/user', headers: { authorization: 'Bearer ' + token } });
  });
  const issue = async id => {
    const cookies = {};
    const access = await native.runAsSystem(() => auth.setAuthTokens(id,
      { cookie: (name, value) => { cookies[name] = value; } }));
    return { access, refresh: cookies.refreshToken };
  };
  const httpAccess = async (middleware, headers, pathname = '/protected') => {
    const app = nativeRequire('express')();
    app.use(passport.initialize());
    app.get(pathname, middleware, (req, res) => res.status(pathname.startsWith('/images/') || req.user ? 200 : 401).end());
    const server = await new Promise(resolve => { const active = app.listen(0, '127.0.0.1', () => resolve(active)); });
    try {
      const response = await fetch('http://127.0.0.1:' + server.address().port + pathname, { headers });
      await response.arrayBuffer();
      return response.status;
    } finally { await new Promise(resolve => server.close(resolve)); }
  };
  const cookieAccess = token => httpAccess(share, token ? { cookie: 'refreshToken=' + token } : {});
  const imageAccess = (token, id) => httpAccess(image, token ? { cookie: 'refreshToken=' + token } : {}, '/images/' + id + '/fixture.png');
  const bearerAccess = token => httpAccess(requireJwtAuth, token ? { authorization: 'Bearer ' + token } : {});
  const agentAccess = token => httpAccess(agents.requireRemoteAgentAuth,
    token ? { authorization: 'Bearer ' + token } : {});
  const { privateKey, publicKey } = require('node:crypto').generateKeyPairSync('rsa', { modulusLength: 2048 });
  const publicJwk = { ...publicKey.export({ format: 'jwk' }), kid: 'fixture-key', alg: 'RS256', use: 'sig' };
  const keyServer = require('node:https').createServer({
    key: fs.readFileSync('/fixture/oidc-key.pem'), cert: fs.readFileSync('/fixture/oidc-cert.pem'),
  }, (req, res) => {
    if (req.url !== '/jwks') { res.writeHead(404).end(); return; }
    res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ keys: [publicJwk] }));
  });
  await new Promise(resolve => keyServer.listen(0, '127.0.0.1', resolve));
  const close = async () => { keyServer.closeAllConnections(); await new Promise(resolve => keyServer.close(resolve)); };
  try {
    const oidc = { enabled: true, issuer: 'https://identity.example.test',
      audience: 'fixture-agents', jwksUri: 'https://127.0.0.1:' + keyServer.address().port + '/jwks' };
    const signedAgents = load('api/server/routes/agents/middleware.js', {
      endpoints: { agents: {
        remoteApi: { auth: { apiKey: { enabled: false }, oidc: { ...oidc, scope: 'agent:use' } } },
        managementApi: { auth: { oidc, clients: [
          { clientId: 'daily-machine', subject: 'daily-machine', userId: '0123456789abcdef01234567', tenantId: 'fixture-suite' },
          { clientId: 'owner-machine', subject: 'owner-machine', userId: '0123456789abcdef01234568', tenantId: 'fixture-suite' },
        ] } },
      } },
    });
    const signOidc = (claims, options = {}) => jwt.sign({ scope: 'agent:use', ...claims }, privateKey,
      { algorithm: 'RS256', keyid: 'fixture-key', issuer: oidc.issuer, audience: oidc.audience,
        expiresIn: '5m', ...options });
    const oidcAccess = token => httpAccess(signedAgents.requireRemoteAgentAuth,
      token ? { authorization: 'Bearer ' + token } : {});
    const machineAccess = token => httpAccess(signedAgents.requireAgentManagementAuth,
      token ? { authorization: 'Bearer ' + token } : {});
    return { sign, authenticate, issue, cookieAccess, imageAccess, bearerAccess, agentAccess, signOidc, oidcAccess, machineAccess, close, load };
  } catch (error) { await close(); throw error; }
};
