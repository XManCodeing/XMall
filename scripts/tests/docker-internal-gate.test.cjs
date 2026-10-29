'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { readFileSync } = require('node:fs');
const { gate } = require('../docker-internal-gate.cjs');

function fixture() {
  const repo = { id: 42, full_name: 'XManCodeing/XMall', fork: false, owner: { type: 'User' } };
  const pkg = { id: 7, name: 'xmall-internal-private', package_type: 'container', visibility: 'private', repository: repo };
  const calls = [];
  const f = {
    repo, pkg, calls, mode: 'bootstrap', allowCreate: true,
    context: { repo: { owner: 'XManCodeing', repo: 'XMall' }, eventName: 'workflow_dispatch', ref: 'refs/heads/main' },
    github: {
      rest: { repos: { get: async () => ({ data: repo }) } },
      paginate: async () => { throw new Error('Account-wide package listing must not be used'); },
      request: async (route, params) => { calls.push({ kind: 'detail', route, params }); return { data: pkg }; },
    },
  };
  return f;
}

function absent(f, status = 404) {
  f.github.request = async () => { f.calls.push({ kind: 'detail' }); throw Object.assign(new Error(`HTTP ${status}`), { status }); };
}

test('existing package must be private and linked without account-wide listing', async () => {
  for (const mode of ['private', 'bootstrap']) {
    const f = fixture(); f.mode = mode;
    assert.deepEqual(await gate(f), { image: 'ghcr.io/xmancodeing/xmall-internal-private', bootstrapMarker: false, packageId: '7' });
    assert.deepEqual(f.calls.map((c) => c.kind), ['detail']);
    assert.equal(f.calls[0].route, 'GET /users/{owner}/packages/container/{package_name}');
    assert.deepEqual(f.calls[0].params, { owner: 'XManCodeing', package_name: 'xmall-internal-private' });
  }
});

test('explicit initial bootstrap alone permits an empty marker after detail 404', async () => {
  const f = fixture(); absent(f);
  assert.deepEqual(await gate(f), { image: 'ghcr.io/xmancodeing/xmall-internal-private', bootstrapMarker: true });
  assert.deepEqual(f.calls.map((c) => c.kind), ['detail']);
  f.mode = 'private';
  await assert.rejects(gate(f), /404/);
  f.mode = 'bootstrap'; f.allowCreate = false;
  await assert.rejects(gate(f), /404/);
  f.allowCreate = true; f.expected = '7';
  await assert.rejects(gate(f), /404/);
});

for (const status of [400, 401, 403, 429, 500, undefined]) {
  test(`detail ${status} never authorizes bootstrap`, async () => {
    const f = fixture(); absent(f, status);
    if (status === undefined) f.github.request = async () => { throw new Error('network failure'); };
    await assert.rejects(gate(f));
  });
}

test('marker authorization is never application authorization', async () => {
  const f = fixture(); absent(f);
  assert.equal((await gate(f)).bootstrapMarker, true);
  // The confirmation step does not opt in to another bootstrap, even on 404.
  delete f.allowCreate;
  await assert.rejects(gate(f), /404/);
  f.github.request = async () => ({ data: f.pkg });
  assert.equal((await gate(f)).packageId, '7');
  for (const visibility of ['public', 'internal', undefined]) {
    f.pkg.visibility = visibility;
    await assert.rejects(gate(f), /must be private/);
  }
});

const invalid = {
  'validate mode': (f) => { f.mode = 'validate'; },
  'unknown mode': (f) => { f.mode = 'public'; },
  'other branch': (f) => { f.context.ref = 'refs/heads/dev'; },
  'other repository': (f) => { f.context.repo.owner = 'other'; },
  'wrong trigger': (f) => { f.context.eventName = 'push'; },
  'fork': (f) => { f.repo.fork = true; },
  'repo mismatch': (f) => { f.repo.full_name = 'other/repo'; },
  'public': (f) => { f.pkg.visibility = 'public'; },
  'internal': (f) => { f.pkg.visibility = 'internal'; },
  'unlinked': (f) => { delete f.pkg.repository; },
  'wrong linked repo': (f) => { f.pkg.repository = { id: 43, full_name: 'XManCodeing/XMall' }; },
  'wrong linked name': (f) => { f.pkg.repository = { id: 42, full_name: 'other/repo' }; },
  'retired public package name': (f) => { f.pkg.name = 'xmall-internal'; },
  'wrong package': (f) => { f.pkg.name = 'xmall'; },
  'wrong package type': (f) => { f.pkg.package_type = 'npm'; },
  'missing package id': (f) => { delete f.pkg.id; },
  'changed identity': (f) => { f.expected = '8'; },
};
for (const [name, mutate] of Object.entries(invalid)) {
  test(`fails closed: ${name}`, async () => {
    const f = fixture(); mutate(f);
    await assert.rejects(gate(f));
  });
}

test('organization owner uses organization detail route', async () => {
  const f = fixture(); f.repo.owner.type = 'Organization';
  await gate(f);
  assert.ok(f.calls.every((call) => call.route.startsWith('GET /orgs/')));
});

test('workflow isolates validate and gates marker and exact-archive application publication', () => {
  const workflow = readFileSync(new URL('../../.github/workflows/docker-internal.yml', `file://${__filename}`), 'utf8');
  const [validate, publish] = workflow.split('  private-push:');
  assert.match(workflow, /- validate\n\s+- private\n\s+- bootstrap/);
  assert.match(validate, /if: inputs.mode == 'validate'/);
  assert.doesNotMatch(validate, /login|push: true|packages: write|secrets\.GITHUB_TOKEN|docker:\/\//);
  assert.match(publish, /github.ref == 'refs\/heads\/main' && github.repository == 'XManCodeing\/XMall'/);
  assert.match(publish, /packages: write/);
  assert.match(publish, /if: steps.preflight.outputs.bootstrapMarker == 'true'/);
  assert.equal((publish.match(/allowCreate: true/g) || []).length, 1);
  assert.match(publish, /mode: process.env.MODE, expected: process.env.EXPECTED/);
  assert.match(workflow, /DOCKER_BUILD_RECORD_UPLOAD: 'false'/);
  assert.match(workflow, /DOCKER_BUILD_SUMMARY: 'false'/);
  assert.doesNotMatch(workflow, /upload-artifact|cache-to:|push: true|:latest|docker\/login-action/);
  assert.equal((workflow.match(/linux\/amd64,linux\/arm64/g) || []).length, 2);
  assert.equal((workflow.match(/!\.env\*/g) || []).length, 2);
  const preflight = publish.indexOf('Require private package');
  const marker = publish.indexOf('Bootstrap empty marker');
  const confirm = publish.indexOf('Confirm actual private');
  const build = publish.indexOf('Build both architectures');
  const smoke = publish.indexOf('python3 scripts/docker-smoke.py');
  const recheck = publish.indexOf('Recheck private package immediately');
  const push = publish.indexOf('Push exact tested archive');
  assert.ok(preflight < marker && marker < confirm && confirm < build && build < smoke && smoke < recheck && recheck < push);
  assert.match(publish.slice(marker, confirm), /'FROM scratch'/);
  assert.match(publish.slice(marker, confirm), /org.opencontainers.image.source="https:\/\/github.com\/XManCodeing\/XMall"/);
  assert.match(publish.slice(marker, confirm), /xmall-marker-context/);
  assert.match(publish.slice(push), /skopeo copy --all --preserve-digests/);
  assert.match(publish.slice(push), /"oci-archive:\$RUNNER_TEMP\/xmall-internal.oci.tar" "docker:\/\/\$IMAGE:\$IMAGE_TAG"/);
  assert.match(publish.slice(push), /IMAGE_TAG: internal-\$\{\{ github.sha \}\}-\$\{\{ github.run_id \}\}-\$\{\{ github.run_attempt \}\}/);
  assert.match(publish.slice(push), /python3 scripts\/docker-internal-registry.py/);
});
