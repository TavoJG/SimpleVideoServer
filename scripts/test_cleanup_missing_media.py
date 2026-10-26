import contextlib
from contextlib import closing
import io
from pathlib import Path
import sqlite3
import tempfile
import unittest

from cleanup_missing_media import plan, run


class CleanupTests(unittest.TestCase):
    def test_preview_apply_backup_and_preserve_existing_records(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'media'
            root.mkdir()
            existing = root / 'present.jpg'
            existing.write_bytes(b'keep')
            db_path = Path(tmp) / 'library.sqlite3'
            rows = [(1, str(root / 'gone.jpg'), str(root), 1),
                    (2, str(existing), str(root), 1),
                    (3, str(root / 'unscanned.jpg'), str(root), 0),
                    (4, '/other/gone.jpg', '/other', 1)]
            with closing(sqlite3.connect(db_path)) as db, db:
                db.execute('CREATE TABLE videos(id INTEGER PRIMARY KEY,path TEXT,root TEXT,missing INTEGER)')
                db.executemany('INSERT INTO videos VALUES(?,?,?,?)', rows)
            args = ['--db', str(db_path), '--root', str(root)]
            with contextlib.redirect_stdout(io.StringIO()):
                run(args)
            with closing(sqlite3.connect(db_path)) as db, db:
                self.assertEqual(db.execute('SELECT COUNT(*) FROM videos').fetchone()[0], 4)
            with contextlib.redirect_stdout(io.StringIO()):
                run(args + ['--apply'])
            with closing(sqlite3.connect(db_path)) as db, db:
                self.assertEqual([r[0] for r in db.execute('SELECT id FROM videos ORDER BY id')], [2, 3, 4])
            backup, = Path(tmp).glob('*.before-missing-cleanup-*.sqlite3')
            with closing(sqlite3.connect(backup)) as db:
                self.assertEqual(db.execute('SELECT COUNT(*) FROM videos').fetchone()[0], 4)
            self.assertEqual(existing.read_bytes(), b'keep')

    def test_pending_recovery_aborts(self):
        with closing(sqlite3.connect(':memory:')) as db:
            db.execute('CREATE TABLE pending_deletions(id INTEGER)')
            db.execute('INSERT INTO pending_deletions VALUES(1)')
            with self.assertRaisesRegex(ValueError, 'recovery'):
                plan(db, Path('/library'))


if __name__ == '__main__':
    unittest.main()
