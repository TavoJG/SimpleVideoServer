import base64
import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import classify_media as cli


class ClassifierTests(unittest.TestCase):
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
        self.db.executescript("""CREATE TABLE categories(name TEXT UNIQUE);
            CREATE TABLE videos(id INTEGER PRIMARY KEY,path TEXT UNIQUE,root TEXT,relative_path TEXT,
              category TEXT,media_type TEXT,filename TEXT,title TEXT,tags TEXT,
              missing INTEGER DEFAULT 0,mtime REAL,size_bytes INTEGER,updated_at TEXT);""")
        self.db.execute("INSERT INTO videos(id,path,root,relative_path,category,media_type,filename,title,tags,mtime,size_bytes) VALUES(1,?,?,?,'Uncategorized','image','sample.jpg','Original','old tag',?,?)",
                        (str(self.path), str(self.root), self.path.name, self.path.stat().st_mtime, self.path.stat().st_size))
        self.db.commit()
        self.item = self.db.execute("SELECT * FROM videos").fetchone()

    def result(self):
        return {"category": "Nature", "tags": ["trees"], "reason": "Trees visible"}

    def test_labels_and_model_validation(self):
        labels = self.root / "labels.json"
        for invalid in (["Parent/Child"], ["../escape"], ["Nature/../escape"], ["a", "A"], [".video-library-trash"], ["Uncategorized/Child"]):
            labels.write_text(json.dumps(invalid))
            with self.assertRaises(ValueError):
                cli.load_labels(labels, True)
        labels.write_text('["Nature", "People"]')
        self.assertEqual(len(cli.load_labels(labels, True)), 2)
        for bad in ({"category": "Invented", "tags": [], "reason": ""}, {"category": None, "tags": ["invented"], "reason": ""}):
            with self.assertRaises(ValueError):
                cli.validate_result(bad, ["People"], ["trees"])
        self.assertIsNone(cli.validate_result({"category": None, "tags": [], "reason": "Ambiguous"}, ["People"], ["trees"])["category"])

    def test_move_collision_and_replace_tags(self):
        folder = self.root / "Nature"
        folder.mkdir(parents=True)
        occupied = folder / "sample.jpg"
        occupied.write_bytes(b"occupied")
        moved = cli.apply_result(self.db, self.item, cli.identity(self.path), self.result())
        self.assertEqual(occupied.read_bytes(), b"occupied")
        self.assertTrue(Path(moved).exists())
        self.assertFalse(self.path.exists())
        row = self.db.execute("SELECT * FROM videos").fetchone()
        self.assertEqual((row["category"], json.loads(row["tags"]), row["title"]), ("Nature", ["trees"], "Original"))
        self.assertEqual(row["relative_path"], "Nature/sample_1.jpg")

    def test_json_tags_preserve_commas_when_adding(self):
        self.db.execute("UPDATE videos SET tags=?", (json.dumps(["Art, Music", "Existing"]),))
        self.db.commit()
        snapshot = self.db.execute("SELECT * FROM videos").fetchone()
        cli.apply_result(self.db, snapshot, cli.identity(self.path), {"category": None, "tags": ["Art, Music", "New"], "reason": ""}, tag_mode="add")
        self.assertEqual(json.loads(self.db.execute("SELECT tags FROM videos").fetchone()[0]), ["Art, Music", "Existing", "New"])
        with self.assertRaises(ValueError):
            cli.destination(self.root, "Parent/Child")

    def test_rollback_restores_file(self):
        self.db.executescript("CREATE TRIGGER fail_update BEFORE UPDATE ON videos BEGIN SELECT RAISE(ABORT,'test failure'); END;")
        with self.assertRaises(sqlite3.Error):
            cli.apply_result(self.db, self.item, cli.identity(self.path), self.result())
        self.assertTrue(self.path.exists())
        self.assertFalse((self.root / "Nature/sample.jpg").exists())
        self.assertEqual(self.db.execute("SELECT tags FROM videos").fetchone()[0], "old tag")

    def test_stale_and_symlink_destination(self):
        stamp = cli.identity(self.path)
        self.path.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            cli.apply_result(self.db, self.item, stamp, self.result())
        self.assertEqual(self.db.execute("SELECT tags FROM videos").fetchone()[0], "old tag")
        (self.root / "Nature").symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(ValueError):
            cli.apply_result(self.db, self.item, cli.identity(self.path), self.result())

    def test_tag_only_and_category_abstention(self):
        cli.apply_result(self.db, self.item, cli.identity(self.path), {"category": None, "tags": [], "reason": ""})
        row = self.db.execute("SELECT * FROM videos").fetchone()
        self.assertEqual(json.loads(row["tags"]), [])
        self.assertEqual(row["path"], str(self.path))
        cli.apply_result(self.db, row, cli.identity(self.path), self.result(), do_categories=False)
        row = self.db.execute("SELECT * FROM videos").fetchone()
        self.assertEqual(json.loads(row["tags"]), ["trees"])
        self.assertEqual(row["category"], "Uncategorized")

    def test_cli_report_and_apply(self):
        categories = self.root / "categories.json"; categories.write_text('["Nature"]')
        tags = self.root / "tags.json"; tags.write_text('["trees"]')
        args = ["--db", str(self.db_path), "--categories", str(categories), "--tags", str(tags), "--model", "vision:test"]
        with patch("classify_media.previews", return_value=["preview"]), patch("classify_media.infer", return_value=self.result()):
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                self.assertEqual(cli.main(args), 0)
            self.assertFalse(json.loads(out.getvalue())["applied"])
            self.assertTrue(self.path.exists())
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(cli.main(args + ["--apply"]), 0)
            self.assertEqual(json.loads(self.db.execute("SELECT tags FROM videos").fetchone()[0]), ["trees"])

    def test_visual_inference_and_invalid_output(self):
        envelope = {"message": {"content": json.dumps(self.result())}}
        with patch("classify_media.urllib.request.urlopen", return_value=io.BytesIO(json.dumps(envelope).encode())) as call:
            result = cli.infer(["image"], ["Nature"], ["trees"], "vision:test", "http://gpu", 10)
            payload = json.loads(call.call_args.args[0].data)
            self.assertEqual(payload["messages"][0]["images"], ["image"])
            self.assertNotIn("sample.jpg", payload["messages"][0]["content"])
            self.assertEqual(result["tags"], ["trees"])
        with patch("classify_media.urllib.request.urlopen", return_value=io.BytesIO(b'{"message":{"content":"bad json"}}')):
            with self.assertRaises(ValueError):
                cli.infer(["image"], [], ["trees"], "vision:test", "http://gpu", 10)

    def test_partial_errors_and_exclusion_of_missing(self):
        second = self.root / "second.jpg"; second.write_bytes(b"sample")
        self.db.execute("INSERT INTO videos SELECT 2,?,root,'second.jpg',category,media_type,'second.jpg',title,tags,missing,?,size_bytes,updated_at FROM videos WHERE id=1", (str(second), second.stat().st_mtime))
        self.db.execute("INSERT INTO videos SELECT 3,'missing.jpg',root,relative_path,category,media_type,filename,title,tags,1,mtime,size_bytes,updated_at FROM videos WHERE id=1")
        self.db.commit()
        tags = self.root / "tags.json"; tags.write_text('["trees"]')
        result = {"category": None, "tags": ["trees"], "reason": ""}
        with patch("classify_media.previews", return_value=["preview"]), patch("classify_media.infer", side_effect=[OSError("offline"), result]) as infer:
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                code = cli.main(["--db", str(self.db_path), "--tags", str(tags), "--model", "vision:test", "--apply"])
            self.assertEqual(code, 1)
            self.assertEqual(infer.call_count, 2)
            records = [json.loads(line) for line in out.getvalue().splitlines()]
            self.assertIn("error", records[0])
            self.assertTrue(records[1]["applied"])
            self.assertEqual(self.db.execute("SELECT tags FROM videos WHERE id=1").fetchone()[0], "old tag")
            self.assertEqual(json.loads(self.db.execute("SELECT tags FROM videos WHERE id=2").fetchone()[0]), ["trees"])

    @unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "FFmpeg required")
    def test_real_previews_and_short_video(self):
        image = self.root / "large.jpg"
        subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=1000x600", "-frames:v", "1", str(image)], check=True)
        previews = cli.previews({"path": str(image), "media_type": "image"}, 8, 15)
        self.assertTrue(base64.b64decode(previews[0]).startswith(b"\xff\xd8"))
        video = self.root / "short.mp4"
        subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=0.4", "-c:v", "mpeg4", str(video)], check=True)
        self.assertEqual(len(cli.previews({"path": str(video), "media_type": "video"}, 8, 15)), 1)
        for duration in (0.1, 2, 20):
            self.assertTrue(all(0 < at < duration for at in cli.sample_times(duration)))


if __name__ == "__main__":
    unittest.main()
