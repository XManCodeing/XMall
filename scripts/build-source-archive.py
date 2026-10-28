#!/usr/bin/env python3
"""Build a source upload ZIP from explicit source roots, without reading runtime secrets."""

from pathlib import Path
import hashlib
import os
import zipfile

ROOT = Path(__file__).resolve().parents[1]
ROOT_FILES = {
    'README.md', 'LICENSE', 'go.mod', 'go.sum', 'Dockerfile',
    '.gitignore', '.dockerignore', '.goreleaser.yaml', 'config.yml.example',
}
SOURCE_DIRS = {'cmd', 'internal', 'frontend', 'scripts', 'assets', '.github'}
EXCLUDED_DIRS = {
    '.git', '.idea', '.vscode', '.cursor', '.agents', 'node_modules',
    'dist', 'coverage', '.cache', '.vite', '.pnpm-store', '__pycache__',
    'db', 'logs', 'uploads', 'backups', 'backup', '.DS_Store',
}
EXCLUDED_SUFFIXES = (
    '.db', '.db-wal', '.db-shm', '.sqlite', '.sqlite3', '.log', '.sql',
    '.sql.gz', '.key', '.pem', '.p12', '.pfx', '.zip', '.tar', '.tar.gz',
    '.bak', '.backup', '.pyc', '.tsbuildinfo', '.local',
)


def excluded(name):
    lower = name.lower()
    return (
        lower.startswith(('.env', 'config.yml', 'config.yaml', '._'))
        or name == '.DS_Store'
        or lower.endswith(EXCLUDED_SUFFIXES)
    )


def main():
    paths = []
    skipped = []
    for name in sorted(ROOT_FILES):
        path = ROOT / name
        if path.is_symlink() or not path.is_file():
            raise RuntimeError(f'Missing or symlinked required input: {name}')
        paths.append(path)
    for name in sorted(SOURCE_DIRS):
        base = ROOT / name
        if base.is_symlink() or not base.is_dir():
            raise RuntimeError(f'Missing or symlinked source directory: {name}')
        for directory, dirs, files in os.walk(base, followlinks=False):
            parent = Path(directory)
            for child in list(dirs):
                candidate = parent / child
                if child in EXCLUDED_DIRS or excluded(child) or candidate.is_symlink():
                    dirs.remove(child)
                    skipped.append(str(candidate.relative_to(ROOT)) + '/')
            for child in sorted(files):
                candidate = parent / child
                if candidate.is_symlink() or excluded(child):
                    skipped.append(str(candidate.relative_to(ROOT)))
                    continue
                if candidate.stat().st_size > 25 * 1024 * 1024:
                    raise RuntimeError(f'Unexpected large source file; review first: {candidate.relative_to(ROOT)}')
                paths.append(candidate)
    for child in ROOT.iterdir():
        if child.name not in ROOT_FILES | SOURCE_DIRS:
            skipped.append(child.name + ('/' if child.is_dir() else ''))

    output = ROOT / 'dist' / 'source-upload'
    output.mkdir(parents=True, exist_ok=True)
    archive = output / 'xmall-source.zip'
    with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as bundle:
        for path in sorted(paths):
            bundle.write(path, str(path.relative_to(ROOT)))
    with zipfile.ZipFile(archive) as bundle:
        assert bundle.testzip() is None
        names = bundle.namelist()
        assert set(ROOT_FILES).issubset(names)
        assert '.github/workflows/release.yml' in names
        assert 'frontend/user/pnpm-lock.yaml' in names
        assert 'frontend/admin/pnpm-lock.yaml' in names
        for name in names:
            if name != 'config.yml.example':
                assert not any(excluded(part) or part in EXCLUDED_DIRS for part in Path(name).parts), name
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    (output / 'included-files.txt').write_text('\n'.join(sorted(names)) + '\n')
    # The exclusion report contains local filenames; keep it local, outside the ZIP.
    (output / 'excluded-files.txt').write_text('\n'.join(sorted(skipped)) + '\n')
    (output / 'SHA256SUMS.txt').write_text(f'{digest}  {archive.name}\n')
    print(f'Archive: {archive}')
    print(f'Files: {len(names)}; size: {archive.stat().st_size / 1024 / 1024:.2f} MiB')
    print(f'SHA256: {digest}')
    print('ZIP integrity and excluded-path checks passed; no content/history secret audit implied.')
    print('Root entries not selected: ' + ', '.join(sorted(p.name for p in ROOT.iterdir() if p.name not in ROOT_FILES | SOURCE_DIRS)))


if __name__ == '__main__':
    main()
