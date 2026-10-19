import base64
import contextlib
import io
import json
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import classify_media as cli


class CategoryTaggerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.path = self.root / "sample.jpg"
        self.path.write_bytes(b"sample")
        self.db_path = self.root / "library.sqlite"
        self.db = sqlite3.connect(self.db_path)
        self.db.row_factory = sqlite3.Row
        self.addCleanup(self.db.close)
        self.db.executescript("""CREATE TABLE categories(id INTEGER PRIMARY KEY,name TEXT UNIQUE COLLATE NOCASE,tags TEXT,updated_at TEXT);
            CREATE TABLE videos(id INTEGER PRIMARY KEY,path TEXT UNIQUE,root TEXT,category TEXT,media_type TEXT,
                missing INTEGER DEFAULT 0,mtime REAL,size_bytes INTEGER);
            INSERT INTO categories VALUES(1,'Nature','["Existing","Art, Music"]',NULL);
            INSERT INTO categories VALUES(2,'Empty','["Keep"]',NULL);""")
        self.db.execute("INSERT INTO videos VALUES(1,?,?,'Nature','image',0,?,?)",
                        (str(self.path), str(self.root), self.path.stat().st_mtime, self.path.stat().st_size))
        self.db.commit()
        self.category = self.db.execute("SELECT * FROM categories WHERE id=1").fetchone()
        self.item = self.db.execute("SELECT * FROM videos").fetchone()
        self.tag_path = self.root / "tags.json"
        self.tag_path.write_text('["trees", "art, music"]')

    def stored(self):
        return json.loads(self.db.execute("SELECT tags FROM categories WHERE id=1").fetchone()[0])

    def evidence(self):
        return [(self.item, cli.identity(self.path))]

    def test_add_only_and_idempotence(self):
        before = dict(self.item)
        merged, added = cli.apply_result(self.db, self.category, self.evidence(), ["art, music", "trees", "trees"])
        self.assertEqual(merged, ["Existing", "Art, Music", "trees"])
        self.assertEqual(added, ["trees"])
        self.assertEqual(cli.apply_result(self.db, self.category, self.evidence(), ["trees"])[1], [])
        self.assertEqual(dict(self.db.execute("SELECT * FROM videos").fetchone()), before)
        self.assertEqual(self.path.read_bytes(), b"sample")

    def test_empty_suggestions_keep_existing_tags(self):
        before = tuple(self.db.execute("SELECT * FROM categories WHERE id=1").fetchone())
        cli.apply_result(self.db, self.category, self.evidence(), [])
        self.assertEqual(tuple(self.db.execute("SELECT * FROM categories WHERE id=1").fetchone()), before)

    def test_merge_latest_tags_not_stale_snapshot(self):
        self.db.execute("UPDATE categories SET tags='[\"Concurrent\"]' WHERE id=1")
        self.db.commit()
        cli.apply_result(self.db, self.category, self.evidence(), ["trees"])
        self.assertEqual(self.stored(), ["Concurrent", "trees"])

    def test_changed_media_or_category_rejected(self):
        evidence = self.evidence()
        self.path.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            cli.apply_result(self.db, self.category, evidence, ["trees"])
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])
        self.db.execute("UPDATE categories SET name='Renamed' WHERE id=1")
        self.db.commit()
        with self.assertRaises(ValueError):
            cli.apply_result(self.db, self.category, [], ["trees"])

    def test_transaction_rollback(self):
        self.db.executescript("CREATE TRIGGER reject_tags BEFORE UPDATE ON categories BEGIN SELECT RAISE(ABORT,'test'); END;")
        with self.assertRaises(sqlite3.Error):
            cli.apply_result(self.db, self.category, self.evidence(), ["trees"])
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])

    def run_cli(self, extra=(), results=None):
        output = io.StringIO()
        results = results if results is not None else [{"tags": ["trees"], "reason": "Trees"}]
        with patch("classify_media.previews", return_value=["preview"]), patch("classify_media.infer", side_effect=results), contextlib.redirect_stdout(output):
            code = cli.main(["--db", str(self.db_path), "--tags", str(self.tag_path), "--model", "vision:test", *extra])
        return code, [json.loads(line) for line in output.getvalue().splitlines()]

    def test_cli_category_report_and_apply(self):
        code, records = self.run_cli()
        self.assertEqual(code, 0)
        self.assertEqual(records[0]["category"], "Nature")
        self.assertEqual(records[0]["added_tags"], ["trees"])
        self.assertFalse(records[0]["applied"])
        self.assertEqual(records[1]["skipped"], "No active indexed media")
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])
        code, records = self.run_cli(["--apply", "--source-category", "Nature"])
        self.assertEqual(code, 0)
        self.assertTrue(records[0]["applied"])
        self.assertEqual(self.stored(), ["Existing", "Art, Music", "trees"])

    def test_union_partial_failure_and_missing_excluded(self):
        second = self.root / "second.jpg"; second.write_bytes(b"sample")
        self.db.execute("INSERT INTO videos SELECT 2,?,root,category,media_type,0,?,size_bytes FROM videos WHERE id=1", (str(second), second.stat().st_mtime))
        self.db.execute("INSERT INTO videos SELECT 3,'missing',root,category,media_type,1,mtime,size_bytes FROM videos WHERE id=1")
        self.db.commit()
        code, records = self.run_cli(["--apply"], [OSError("offline"), {"tags": ["trees"], "reason": "Trees"}])
        self.assertEqual(code, 1)
        self.assertTrue(records[0]["applied"])
        self.assertEqual(records[0]["errors"][0]["media_id"], 1)
        self.assertEqual(self.stored(), ["Existing", "Art, Music", "trees"])
        code, records = self.run_cli(["--source-category", "Nature"], [{"tags": ["trees"], "reason": ""}, {"tags": ["art, music"], "reason": ""}])
        self.assertEqual(records[0]["suggested_tags"], ["trees", "art, music"])

    def test_validation_and_removed_flags(self):
        with self.assertRaises(ValueError):
            cli.validate_result({"category": "New", "tags": [], "reason": ""}, ["trees"])
        with self.assertRaises(ValueError):
            cli.validate_result({"tags": ["invented"], "reason": ""}, ["trees"])
        for flag in ("--categories", "--tag-mode", "--id"):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                cli.main(["--tags", str(self.tag_path), flag, "old"])
            self.assertEqual(error.exception.code, 2)

    def test_inference_is_visual_tags_only(self):
        envelope = {"message": {"content": '{"tags":["trees"],"reason":"Trees"}'}}
        with patch("classify_media.urllib.request.urlopen", return_value=io.BytesIO(json.dumps(envelope).encode())) as call:
            self.assertEqual(cli.infer(["image"], ["trees"], "vision:test", "http://gpu", 10)["tags"], ["trees"])
            payload = json.loads(call.call_args.args[0].data)
            self.assertEqual(set(payload["format"]["properties"]), {"tags", "reason"})
            self.assertEqual(payload["messages"][0]["images"], ["image"])
            self.assertNotIn("sample.jpg", payload["messages"][0]["content"])

    def test_even_media_sampling(self):
        self.assertEqual(cli.sample_media(list(range(10)), 3), [0, 4, 9])
        self.assertEqual(cli.sample_media(list(range(10)), 0), list(range(10)))

    @unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "FFmpeg required")
    def test_real_image_and_short_video_previews(self):
        image = self.root / "large.jpg"
        subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=1000x600", "-frames:v", "1", str(image)], check=True)
        self.assertTrue(base64.b64decode(cli.previews({"path": str(image), "media_type": "image"}, 8, 15)[0]).startswith(b"\xff\xd8"))
        video = self.root / "short.mp4"
        subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=0.4", "-c:v", "mpeg4", str(video)], check=True)
        self.assertEqual(len(cli.previews({"path": str(video), "media_type": "video"}, 8, 15)), 1)


if __name__ == "__main__":
    unittest.main()
