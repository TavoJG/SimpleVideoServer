#!/usr/bin/env python3
"""Report exact media duplicates; pass --delete to permanently remove extra copies."""

import argparse
from collections import defaultdict
import hashlib
import os
from pathlib import Path
import stat
import sys


EXTENSIONS = set(".mp4 .m4v .mov .webm .mkv .avi .wmv .flv .mpeg .mpg "
                 ".jpg .jpeg .png .gif .webp .bmp .tif .tiff .avif".split())


def signature(info):
    return (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def digest(path):
    # Refuse symlinks, including a file replaced with one during the scan.
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode):
            raise ValueError(f"Not a regular file: {path}")
        result = hashlib.sha256()
        while chunk := stream.read(1024 * 1024):
            result.update(chunk)
        after = os.fstat(stream.fileno())
    if signature(before) != signature(after) or signature(after) != signature(path.lstat()):
        raise ValueError(f"File changed while hashing: {path}")
    return result.hexdigest(), signature(after)


def candidates(root, recursive):
    for directory, dirs, files in os.walk(root, followlinks=False):
        dirs[:] = sorted(d for d in dirs if not d.startswith('.')
                         and not (Path(directory) / d).is_symlink())
        if not recursive and Path(directory) != root:
            dirs[:] = []
        for name in sorted(files):
            path = Path(directory) / name
            if path.suffix.lower() in EXTENSIONS and stat.S_ISREG(path.lstat().st_mode):
                yield path


def run(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('root', nargs='?', default=os.getenv('VIDEO_ROOT'),
                        help='Media folder (defaults to VIDEO_ROOT)')
    parser.add_argument('--delete', action='store_true', help='Permanently delete extra copies')
    parser.add_argument('--recursive', action='store_true', help='Include nested folders')
    args = parser.parse_args(argv)
    if not args.root:
        parser.error('provide a media folder or set VIDEO_ROOT')
    root = Path(args.root).expanduser().resolve(strict=True)
    if not root.is_dir():
        parser.error('root must be a directory')

    sizes = defaultdict(list)
    for path in candidates(root, args.recursive):
        sizes[path.stat().st_size].append(path)
    groups = []
    # Complete the scan before performing any deletions.
    for paths in sizes.values():
        if len(paths) < 2:
            continue
        hashes = defaultdict(list)
        for path in sorted(paths, key=lambda p: p.relative_to(root).as_posix()):
            sha, snapshot = digest(path)
            hashes[sha].append((path, snapshot))
        groups.extend((sha, matches) for sha, matches in hashes.items() if len(matches) > 1)

    count = total = 0
    for sha, matches in groups:
        keeper, _ = matches[0]
        print(f'SHA-256 {sha}\n  KEEP {str(keeper)!r}')
        for path, snapshot in matches[1:]:
            if args.delete:
                # Recheck both copies before each unlink; abort if either changed.
                current_sha, _ = digest(keeper)
                duplicate_sha, current = digest(path)
                if current_sha != sha or duplicate_sha != sha or current != snapshot:
                    raise ValueError(f'Files changed; refusing deletion: {path}')
                if signature(path.lstat()) != current:
                    raise ValueError(f'File changed; refusing deletion: {path}')
                path.unlink()
            print(f'  {"DELETED" if args.delete else "WOULD DELETE"} {str(path)!r}')
            count += 1
            total += snapshot[2]
    print(f'{len(groups)} duplicate groups; {count} extra copies; {total} logical bytes '
          f'{"removed" if args.delete else "eligible for removal"}.')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(run())
    except (OSError, ValueError) as exc:
        print(f'Error: {exc}', file=sys.stderr)
        sys.exit(1)
