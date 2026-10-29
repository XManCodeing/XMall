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
  // Query only the target package. Listing a personal account's private packages
  // is not supported by every token accepted for this repository's package.
  // A 404 does not prove absence: it may hide a package we cannot access.
  // Explicit bootstrap may therefore write ONLY an empty marker; application
  // publication still requires a successful private/link check after that write.
  let pkg;
  try {
    ({ data: pkg } = await github.request(`GET /${prefix}/{owner}/packages/container/{package_name}`, {
      owner, package_name: PACKAGE,
    }));
  } catch (error) {
    if (error.status !== 404 || mode !== 'bootstrap' || !allowCreate || expected) throw error;
    return { image: `ghcr.io/${owner.toLowerCase()}/${PACKAGE}`, bootstrapMarker: true };
  }
  if (pkg.visibility !== 'private' || pkg.name !== PACKAGE || pkg.package_type !== 'container' ||
      !pkg.id || pkg.repository?.id !== repo.id || pkg.repository?.full_name !== REPOSITORY) {
    throw new Error('Internal package must be private and linked to this repository');
  }
  if (expected && expected !== String(pkg.id)) throw new Error('Package identity changed');
  return { image: `ghcr.io/${owner.toLowerCase()}/${PACKAGE}`, bootstrapMarker: false, packageId: String(pkg.id) };
}

module.exports = { gate };
