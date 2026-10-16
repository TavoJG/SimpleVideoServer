#!/usr/bin/env python3
"""Remove category tags matching their own category name, without changing video tags."""
import argparse
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import sqlite3
import sys


def plan_cleanup(db):
    columns = {row[1] for row in db.execute('PRAGMA table_info(categories)')}
    if not {'name', 'tags'} <= columns:
        raise ValueError('Database must contain categories with a tags column.')
    changes = []
    for name, raw in db.execute('SELECT name,tags FROM categories ORDER BY name COLLATE NOCASE'):
        tags = json.loads(raw)
        if not isinstance(tags, list) or any(not isinstance(tag, str) for tag in tags):
            raise ValueError(f'Invalid category tags for {name!r}; no changes applied.')
        kept = [tag for tag in tags if tag.strip().casefold() != name.strip().casefold()]
        if kept != tags:
            changes.append((name, tags, kept))
    tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    seeds = []
    if {'category_migrations', 'category_tag_seeds'} <= tables:
        rows = db.execute('''SELECT m.root,m.parent,m.subcategory,c.name
            FROM category_migrations m JOIN categories c ON c.name=m.target COLLATE NOCASE
            WHERE m.done=1 AND NOT EXISTS (
                SELECT 1 FROM category_tag_seeds s WHERE s.root=m.root
                    AND s.parent=m.parent AND s.subcategory=m.subcategory)''')
        seeds = [(root, parent, sub) for root, parent, sub, name in rows
                 if parent.strip().casefold() == name.strip().casefold()]
    return changes, seeds


def run(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--db', default=os.getenv('VIDEO_DB', 'videos.sqlite3'))
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--dry-run', action='store_true', help='Preview only (default)')
    mode.add_argument('--apply', action='store_true', help='Back up and apply; stop the server first')
    args = parser.parse_args(argv)
    path = Path(args.db).expanduser().resolve(strict=True)
    with path.open('rb') as lock:
        if args.apply:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise ValueError('Database is in use; stop the server before applying cleanup.') from error
        with sqlite3.connect(path.as_uri() + ('?mode=rw' if args.apply else '?mode=ro'), uri=True, timeout=10) as db:
            changes, seeds = plan_cleanup(db)
            for name, before, after in changes:
                print(json.dumps({'category': name, 'before': before, 'after': after}, ensure_ascii=False))
            removed = sum(len(before) - len(after) for _, before, after in changes)
            print(f'{len(changes)} categories; {removed} self-matching tags; {len(seeds)} pending self-tag seeds.')
            if not args.apply:
                print('Preview only. Use --apply to back up and apply these changes.')
                return 0
            if not changes and not seeds:
                print('No changes needed.')
                return 0
            stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
            backup = path.with_name(path.name + '.before-category-tag-cleanup-' + stamp + '.sqlite3')
            with backup.open('xb'):
                pass
            with sqlite3.connect(backup) as destination:
                db.backup(destination)
            print(f'Backup: {backup}')
            db.execute('BEGIN IMMEDIATE')
            try:
                # Re-read under the SQLite write lock before changing anything.
                changes, seeds = plan_cleanup(db)
                for name, _, after in changes:
                    db.execute('UPDATE categories SET tags=?,updated_at=CURRENT_TIMESTAMP WHERE name=?',
                               (json.dumps(after, ensure_ascii=False), name))
                db.executemany('INSERT INTO category_tag_seeds(root,parent,subcategory) VALUES(?,?,?)', seeds)
                db.commit()
            except BaseException:
                db.rollback()
                raise
            # Verify the invariant inside another read snapshot.
            remaining, pending = plan_cleanup(db)
            if remaining or pending:
                raise ValueError('Cleanup verification failed; inspect the database and backup.')
            print(f'Applied: {len(changes)} categories; '
                  f'{sum(len(before)-len(after) for _, before, after in changes)} tags removed. Video tags unchanged.')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(run())
    except (OSError, sqlite3.Error, ValueError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
