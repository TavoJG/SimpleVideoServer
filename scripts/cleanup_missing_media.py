#!/usr/bin/env python3
"""Preview missing media records; --apply backs up SQLite and removes records only."""

import argparse
from contextlib import closing
from datetime import datetime, timezone
import fcntl
import os
from pathlib import Path
import sqlite3
import sys


def plan(db, root):
    # Recovery journals must be resolved by starting the server before cleanup.
    tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    for table in ('pending_deletions', 'pending_restorations'):
        if table in tables and db.execute(f'SELECT COUNT(*) FROM {table}').fetchone()[0]:
            raise ValueError('Pending recovery operations exist. Start the server to recover them, then stop it and retry.')
    result = []
    for record_id, raw_path in db.execute(
            'SELECT id,path FROM videos WHERE root=? AND missing=1 ORDER BY path', (str(root),)):
        path = Path(raw_path)
        if not path.is_absolute() or not path.is_relative_to(root) or '..' in path.parts:
            raise ValueError(f'Record has an unexpected path: {raw_path!r}')
        try:
            path.lstat()
        except FileNotFoundError:
            result.append((record_id, raw_path))
        # Existing paths, including symlinks, are preserved. Other IO errors abort.
    return result


def run(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--db', default=os.getenv('VIDEO_DB', 'videos.sqlite3'))
    parser.add_argument('--root', default=os.getenv('VIDEO_ROOT'), help='Library root (or VIDEO_ROOT)')
    parser.add_argument('--apply', action='store_true', help='Back up and delete records; stop server first')
    args = parser.parse_args(argv)
    if not args.root:
        parser.error('provide --root or set VIDEO_ROOT')
    root = Path(args.root).expanduser().absolute()
    if not root.is_dir():
        parser.error('media root must be available; check that your media drive is mounted')
    path = Path(args.db).expanduser().resolve(strict=True)
    with path.open('rb') as lock:
        if args.apply:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise ValueError('Stop the server before applying cleanup.') from exc
        with closing(sqlite3.connect(path.as_uri() + ('?mode=rw' if args.apply else '?mode=ro'), uri=True)) as db:
            db.execute('PRAGMA foreign_keys=ON')
            records = plan(db, root)
            for record_id, media_path in records:
                print(f'{record_id}: {media_path!r}')
            print(f'{len(records)} missing records eligible for cleanup.')
            if not args.apply:
                print('Preview only. Use --apply to back up and delete these records. No media files are deleted.')
                return 0
            if not records:
                return 0
            backup = path.with_name(path.name + '.before-missing-cleanup-' +
                                    datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ') + '.sqlite3')
            with backup.open('xb'):
                pass
            with closing(sqlite3.connect(backup)) as destination:
                db.backup(destination)
            print(f'Backup: {backup}')
            db.execute('BEGIN IMMEDIATE')
            try:
                records = plan(db, root)
                db.executemany('DELETE FROM videos WHERE id=? AND root=? AND missing=1',
                               [(record_id, str(root)) for record_id, _ in records])
                db.commit()
            except BaseException:
                db.rollback()
                raise
            print(f'Removed {len(records)} database records. No media files deleted.')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(run())
    except (OSError, ValueError, sqlite3.Error) as exc:
        print(f'Error: {exc}', file=sys.stderr)
        sys.exit(1)
