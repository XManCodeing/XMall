'use strict';

// Kept dependency-free so the same fail-closed gate runs in Actions and node:test.
const APPROVED_REPOSITORY = 'XManCodeing/XMall';
const SHA = /^[0-9a-f]{40}$/;
const NUMBER = '(?:0|[1-9][0-9]*)';
const IDENTIFIER = '(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)';
const SEMVER = new RegExp(`^v${NUMBER}\\.${NUMBER}\\.${NUMBER}(?:-(${IDENTIFIER}(?:\\.${IDENTIFIER})*))?(?:\\+([0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*))?$`);

function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

async function gate({ github, context, config, expected, published = false }) {
  requireValue(config.enabled === 'true', 'Docker release publishing is disabled.');
  const repository = `${context.repo.owner}/${context.repo.repo}`;
  requireValue(repository === APPROVED_REPOSITORY, 'Unapproved source repository.');
  requireValue(['public', 'private', 'internal'].includes(config.visibility), 'Explicit package visibility is required.');
  requireValue(typeof config.package === 'string' && !/[\r\n]/.test(config.package) && /^[a-z0-9]+(?:[._-][a-z0-9]+)*$/.test(config.package), 'Explicit, owner-local package name is required.');
  requireValue(['release', 'workflow_dispatch'].includes(context.eventName), 'Unsupported trigger.');
  if (context.eventName === 'release') {
    requireValue(context.payload.action === 'published', 'Only published releases are supported.');
  }
  const tag = context.eventName === 'release' ? context.payload.release?.tag_name : config.tag;
  requireValue(typeof tag === 'string' && !/[\r\n]/.test(tag) && SEMVER.test(tag), 'Tag must be v-prefixed SemVer, never a branch.');
  // SemVer forbids underscores, so replacing + is lossless and Docker-tag-safe.
  const imageTag = tag.replace('+', '_');
  requireValue(imageTag.length <= 128, 'Version exceeds the Docker tag length limit.');
  const { data: repo } = await github.rest.repos.get(context.repo);
  requireValue(repo.full_name === repository && repo.owner.login === context.repo.owner && !repo.fork, 'Repository identity mismatch or fork.');
  if (context.eventName === 'workflow_dispatch') {
    requireValue(context.ref === `refs/heads/${repo.default_branch}`, 'Manual publishing must run from the default branch.');
  }
  const { data: release } = await github.rest.repos.getReleaseByTag({ ...context.repo, tag });
  requireValue(release.tag_name === tag && release.draft === false && Boolean(release.published_at), 'An existing published, non-draft Release is required.');
  requireValue(release.html_url === `https://github.com/${repository}/releases/tag/${tag}`, 'Release repository mismatch.');
  if (context.eventName === 'release') {
    requireValue(release.id === context.payload.release?.id, 'Release event identity mismatch.');
  }
  const { data: ref } = await github.rest.git.getRef({ ...context.repo, ref: `tags/${tag}` });
  requireValue(ref.ref === `refs/tags/${tag}`, 'Only exact tag refs are allowed.');
  let object = ref.object;
  for (let depth = 0; object.type === 'tag'; depth += 1) {
    requireValue(depth < 8 && SHA.test(object.sha), 'Invalid annotated tag chain.');
    const { data: annotated } = await github.rest.git.getTag({ ...context.repo, tag_sha: object.sha });
    object = annotated.object;
  }
  requireValue(object.type === 'commit' && SHA.test(object.sha), 'Tag must resolve to a commit SHA.');
  const { data: commit } = await github.rest.git.getCommit({ ...context.repo, commit_sha: object.sha });
  requireValue(commit.sha === object.sha, 'Commit identity mismatch.');
  if (context.eventName === 'release') {
    requireValue(context.sha === object.sha, 'Release event SHA differs from current tag; refusing a moved tag.');
  }
  const prefix = repo.owner.type === 'Organization' ? 'orgs' : 'users';
  const route = `/${prefix}/{owner}/packages/container/{package_name}`;
  const parameters = { owner: context.repo.owner, package_name: config.package };
  // Any 401/403/404 propagates. Never create packages or change visibility.
  const { data: pkg } = await github.request(`GET ${route}`, parameters);
  requireValue(pkg.visibility === config.visibility && pkg.package_type === 'container' && pkg.name === config.package, 'Package identity or visibility mismatch.');
  requireValue(pkg.repository?.id === repo.id && pkg.repository?.full_name === repository, 'Package must already be linked to this repository.');
  const result = {
    revision: object.sha,
    releaseId: String(release.id),
    packageId: String(pkg.id),
    image: `ghcr.io/${context.repo.owner.toLowerCase()}/${config.package}`,
    tag,
    imageTag,
    visibility: config.visibility,
  };
  if (expected) {
    for (const key of Object.keys(result)) {
      requireValue(expected[key] === result[key], `Release/package changed during build: ${key}`);
    }
  }
  if (!published) {
    const versions = await github.paginate(`GET ${route}/versions`, { ...parameters, per_page: 100 });
    requireValue(!versions.some((version) => version.metadata?.container?.tags?.includes(imageTag)), 'Fixed version tag already exists; refusing to overwrite.');
  }
  return result;
}

module.exports = { gate };
