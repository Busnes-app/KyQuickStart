'use strict';
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

const contract = 'ky.librechat-account-inspect/1';
const version = '0.1.0';
const refusal = () => new Error('LibreChat native inspection refused');

function validate(request) {
  if (!request || typeof request !== 'object' || Array.isArray(request) ||
      Object.keys(request).sort().join(',') !== 'contract,issuer,subject,user_id' ||
      request.contract !== contract || typeof request.user_id !== 'string' ||
      !/^[0-9a-f]{24}$/.test(request.user_id) || typeof request.issuer !== 'string' ||
      Buffer.byteLength(request.issuer) > 2048 || Buffer.from(request.issuer).toString() !== request.issuer ||
      /[\s\p{Cc}\p{Cf}\\]/u.test(request.issuer) ||
      typeof request.subject !== 'string' || !request.subject ||
      Buffer.byteLength(request.subject) > 255 || /[\s\p{Cc}\p{Cf}]/u.test(request.subject) ||
      Buffer.from(request.subject).toString() !== request.subject) {
    throw refusal();
  }
  let issuer;
  try { issuer = new URL(request.issuer); } catch { throw refusal(); }
  if (issuer.protocol !== 'https:' || !issuer.hostname || issuer.username || issuer.password ||
      issuer.search || issuer.hash || request.issuer.includes('?') || request.issuer.includes('#')) {
    throw refusal();
  }
  return request;
}

function readRequest(file, publicRoots) {
  if (typeof file !== 'string' || !path.isAbsolute(file) || path.normalize(file) !== file ||
      /[\p{Cc}\p{Cf}]/u.test(file) || fs.realpathSync(file) !== file ||
      publicRoots.some(root => file === root || file.startsWith(root + path.sep))) {
    throw refusal();
  }
  const uid = process.getuid();
  for (let parent = path.dirname(file);;) {
    const st = fs.lstatSync(parent);
    if (!st.isDirectory() || (st.uid !== 0 && st.uid !== uid) ||
        ((st.mode & 0o022) && !(st.mode & 0o1000))) { throw refusal(); }
    if (parent === path.parse(parent).root) { break; }
    parent = path.dirname(parent);
  }
  const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const st = fs.fstatSync(fd);
    if (!st.isFile() || st.uid !== uid || (st.mode & 0o7777) !== 0o600 || st.size < 1 || st.size > 4096) {
      throw refusal();
    }
    const buffer = Buffer.alloc(4097);
    let size = 0;
    while (size < buffer.length) {
      const count = fs.readSync(fd, buffer, size, buffer.length - size, size);
      if (!count) { break; }
      size += count;
    }
    if (!size || size > 4096) { throw refusal(); }
    const text = new TextDecoder('utf-8', { fatal: true }).decode(buffer.subarray(0, size));
    return validate(JSON.parse(text));
  } finally { fs.closeSync(fd); }
}

async function inspect(request, native, configuredIssuer) {
  const binding = validate(request);
  if (configuredIssuer !== binding.issuer) { throw refusal(); }
  const user = await native.runAsSystem(() => native.getUserById(binding.user_id,
    '_id provider openidId openidIssuer role'));
  if (!user || user._id?.toString() !== binding.user_id || user.provider !== 'openid' ||
      user.openidId !== binding.subject || user.openidIssuer !== binding.issuer ||
      !['ADMIN', 'USER'].includes(user.role)) { throw refusal(); }
  return { contract: 'ky.librechat-account-report/1', module_version: version,
    user_id: binding.user_id, binding_digest: crypto.createHash('sha256')
      .update(binding.issuer + '\0' + binding.subject).digest('hex'), role: user.role };
}

module.exports = { contract, version, validate, readRequest, inspect };
