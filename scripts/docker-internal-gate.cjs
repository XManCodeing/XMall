'use strict';

const REPOSITORY = 'XManCodeing/XMall';
const PACKAGE = 'xmall-internal';

async function gate({ github, context, mode, allowCreate = false, expected }) {
  if (!['private', 'bootstrap'].includes(mode) || context.eventName !== 'workflow_dispatch' ||
      context.ref !== 'refs/heads/main' || `${context.repo.owner}/${context.repo.repo}` !== REPOSITORY) {
    throw new Error('Internal writes require workflow_dispatch on XManCodeing/XMall main');
  }
  const { data: repo } = await github.rest.repos.get(context.repo);
  if (repo.fork || repo.full_name !== REPOSITORY || !repo.id) throw new Error('Unexpected repository');
  const prefix = repo.owner.type === 'Organization' ? 'orgs' : 'users';
  const owner = context.repo.owner;
  // Exhaust every page before interpreting a package 404. A 403 (including a
  // GITHUB_TOKEN unsupported by this endpoint) must fail, never imply absence.
  const packages = await github.paginate(`GET /${prefix}/{owner}/packages`, {
    owner, package_type: 'container', visibility: 'private', per_page: 100,
  });
  if (!Array.isArray(packages)) throw new Error('Invalid private package list');
  const listed = packages.find((pkg) => pkg.name === PACKAGE);
  let pkg;
  try {
    ({ data: pkg } = await github.request(`GET /${prefix}/{owner}/packages/container/{package_name}`, {
      owner, package_name: PACKAGE,
    }));
  } catch (error) {
    if (error.status !== 404 || listed || mode !== 'bootstrap' || !allowCreate || expected) throw error;
    return { image: `ghcr.io/${owner.toLowerCase()}/${PACKAGE}`, missing: true };
  }
  if (pkg.visibility !== 'private' || pkg.name !== PACKAGE || pkg.package_type !== 'container' ||
      !pkg.id || pkg.repository?.id !== repo.id || pkg.repository?.full_name !== REPOSITORY) {
    throw new Error('Internal package must be private and linked to this repository');
  }
  if (!listed || listed.id !== pkg.id) throw new Error('Package absent from authenticated private list');
  if (expected && expected !== String(pkg.id)) throw new Error('Package identity changed');
  return { image: `ghcr.io/${owner.toLowerCase()}/${PACKAGE}`, missing: false, packageId: String(pkg.id) };
}

module.exports = { gate };
