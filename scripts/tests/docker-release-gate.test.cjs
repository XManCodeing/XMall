'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { readFileSync } = require('node:fs');
const { gate } = require('../docker-release-gate.cjs');

const revision = 'a'.repeat(40);
function fixture() {
  const context = {
    repo: { owner: 'XManCodeing', repo: 'XMall' },
    eventName: 'workflow_dispatch', ref: 'refs/heads/main', payload: {},
  };
  const config = { enabled: 'true', package: 'xmall', visibility: 'private', tag: 'v1.2.3' };
  const repo = { id: 42, full_name: 'XManCodeing/XMall', default_branch: 'main', fork: false, owner: { login: 'XManCodeing', type: 'User' } };
  const release = {
    id: 8, tag_name: config.tag, draft: false, prerelease: false, published_at: '2026-10-29T00:00:00Z',
    html_url: `https://github.com/XManCodeing/XMall/releases/tag/${config.tag}`,
  };
  const ref = { ref: `refs/tags/${config.tag}`, object: { type: 'commit', sha: revision } };
  const pkg = { id: 9, name: 'xmall', package_type: 'container', visibility: 'private', repository: repo };
  const calls = [];
  const github = {
    rest: {
      repos: { get: async () => ({ data: repo }), getReleaseByTag: async () => ({ data: release }) },
      git: {
        getRef: async (args) => { calls.push(args); return { data: ref }; },
        getTag: async () => ({ data: { object: { type: 'commit', sha: revision } } }),
        getCommit: async () => ({ data: { sha: revision } }),
      },
    },
    request: async (route) => { calls.push(route); return { data: pkg }; },
    paginate: async () => [],
  };
  return { github, context, config, repo, release, ref, pkg, calls };
}

function setTag(f, tag) {
  f.config.tag = tag;
  f.release.tag_name = tag;
  f.release.html_url = `https://github.com/XManCodeing/XMall/releases/tag/${tag}`;
  f.ref.ref = `refs/tags/${tag}`;
}

test('published manual Release resolves only exact tag SHA and fixed image', async () => {
  const f = fixture();
  const result = await gate(f);
  assert.equal(result.revision, revision);
  assert.equal(result.image, 'ghcr.io/xmancodeing/xmall');
  assert.equal(result.imageTag, 'v1.2.3');
  assert.equal(f.calls[0].ref, 'tags/v1.2.3');
  assert.match(f.calls[1], /^GET \/users\//);
});

test('published event peels annotated tags and checks event SHA, never target_commitish', async () => {
  const f = fixture();
  f.context.eventName = 'release';
  f.context.sha = revision;
  f.context.payload = { action: 'published', release: { ...f.release, target_commitish: 'main' } };
  f.ref.object = { type: 'tag', sha: 'c'.repeat(40) };
  assert.equal((await gate(f)).revision, revision);
  f.context.sha = 'b'.repeat(40);
  await assert.rejects(gate(f), /moved tag/);
});

test('prerelease and SemVer metadata remain version-only and collision-free', async () => {
  const f = fixture();
  setTag(f, 'v1.2.3-rc.1+build.22');
  f.release.prerelease = true;
  assert.equal((await gate(f)).imageTag, 'v1.2.3-rc.1_build.22');
});

for (const tag of ['main', 'refs/tags/v1.2.3', 'v01.2.3', 'v1.2', '1.2.3', 'v1.2.3-01', 'v1.2.3\n', 'v1.2.3_foo', 'v1.2.3;echo pwn', 'v1.2.3-' + 'x'.repeat(128)]) {
  test(`rejects unsafe/non-SemVer tag ${JSON.stringify(tag)}`, async () => {
    const f = fixture(); setTag(f, tag);
    await assert.rejects(gate(f));
  });
}

const invalidCases = {
  'manual non-default branch': (f) => { f.context.ref = 'refs/heads/dev'; },
  'unset switch': (f) => { delete f.config.enabled; },
  'non-exact switch': (f) => { f.config.enabled = 'TRUE'; },
  'unset visibility': (f) => { f.config.visibility = ''; },
  'unset package': (f) => { f.config.package = ''; },
  'foreign package owner': (f) => { f.config.package = 'other/xmall'; },
  'package output injection': (f) => { f.config.package = 'xmall\n'; },
  'foreign repository': (f) => { f.context.repo.owner = 'attacker'; },
  'fork': (f) => { f.repo.fork = true; },
  'wrong trigger': (f) => { f.context.eventName = 'push'; },
  'draft Release': (f) => { f.release.draft = true; },
  'unpublished Release': (f) => { f.release.published_at = null; },
  'wrong Release repository': (f) => { f.release.html_url = 'https://github.com/other/XMall/releases/tag/v1.2.3'; },
  'branch instead of tag ref': (f) => { f.ref.ref = 'refs/heads/v1.2.3'; },
  'non-commit tag': (f) => { f.ref.object.type = 'tree'; },
  'invalid SHA': (f) => { f.ref.object.sha = 'main'; },
  'mismatched commit': (f) => { f.github.rest.git.getCommit = async () => ({ data: { sha: 'b'.repeat(40) } }); },
  'visibility mismatch': (f) => { f.pkg.visibility = 'public'; },
  'package linked elsewhere': (f) => { f.pkg.repository = { id: 43, full_name: 'other/repo' }; },
  'unlinked package': (f) => { delete f.pkg.repository; },
  'existing fixed version': (f) => { f.github.paginate = async () => [{ metadata: { container: { tags: ['v1.2.3'] } } }]; },
  'annotated tag cycle': (f) => {
    f.ref.object = { type: 'tag', sha: 'c'.repeat(40) };
    f.github.rest.git.getTag = async () => ({ data: { object: f.ref.object } });
  },
  'non-published event': (f) => {
    f.context.eventName = 'release'; f.context.payload = { action: 'created', release: f.release };
  },
  'different event Release': (f) => {
    f.context.eventName = 'release'; f.context.payload = { action: 'published', release: { ...f.release, id: 10 } };
  },
};
for (const [name, mutate] of Object.entries(invalidCases)) {
  test(`fails closed: ${name}`, async () => {
    const f = fixture(); mutate(f);
    await assert.rejects(gate(f));
  });
}
for (const status of [401, 403, 404, 500]) {
  test(`package HTTP ${status} never permits creation or push`, async () => {
    const f = fixture();
    f.github.request = async () => { throw new Error(`HTTP ${status}`); };
    await assert.rejects(gate(f), new RegExp(`HTTP ${status}`));
  });
}

test('organization package route and explicitly public package', async () => {
  const f = fixture();
  f.repo.owner.type = 'Organization';
  f.config.visibility = f.pkg.visibility = 'public';
  await gate(f);
  assert.match(f.calls[1], /^GET \/orgs\//);
});

test('rechecks identity after build and rejects tag/package changes', async () => {
  for (const change of ['revision', 'release', 'package']) {
    const f = fixture();
    const expected = await gate(f);
    if (change === 'revision') {
      f.ref.object.sha = 'b'.repeat(40);
      f.github.rest.git.getCommit = async () => ({ data: { sha: 'b'.repeat(40) } });
    }
    if (change === 'release') f.release.id += 1;
    if (change === 'package') f.pkg.id += 1;
    await assert.rejects(gate({ ...f, expected }), /changed during build/);
  }
});

test('post-push visibility check allows only the existing validated identity', async () => {
  const f = fixture();
  const expected = await gate(f);
  f.github.paginate = async () => { throw new Error('must not query versions after push'); };
  await gate({ ...f, expected, published: true });
  f.pkg.visibility = 'public';
  await assert.rejects(gate({ ...f, expected, published: true }), /visibility mismatch/);
});

test('workflow keeps all layers local and pushes only after exact-archive smoke', () => {
  // Read only this explicitly named workflow, never runtime configuration.
  const workflow = readFileSync(new URL('../../.github/workflows/docker-release.yml', `file://${__filename}`), 'utf8');
  assert.match(workflow, /types: \[published\]/);
  assert.match(workflow, /DOCKER_RELEASE_ENABLED == 'true'/);
  assert.match(workflow, /linux\/amd64,linux\/arm64/);
  assert.match(workflow, /DOCKER_BUILD_RECORD_UPLOAD: 'false'/);
  assert.doesNotMatch(workflow, /upload-artifact|cache-to:|push: true|:latest/);
  assert.match(workflow, /sparse-checkout-cone-mode: false/);
  assert.match(workflow, /!\.env\*/);
  assert.match(workflow, /ref: \$\{\{ steps.gate.outputs.revision \}\}/);
  const build = workflow.indexOf('Build both architectures');
  const smoke = workflow.indexOf('python3 scripts/docker-smoke.py');
  const recheck = workflow.indexOf('Recheck immutable Release');
  const push = workflow.indexOf('skopeo copy --all --preserve-digests');
  assert.ok(workflow.indexOf('python3 scripts/check-release-inputs.py') < build);
  assert.ok(build < smoke && smoke < recheck && recheck < push);
});
