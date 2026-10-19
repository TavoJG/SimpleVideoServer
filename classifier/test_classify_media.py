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
            code = cli.main(["--db", str(self.db_path), "--tags", str(self.tag_path), "--model", "vision:test", "--quiet", *extra])
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
        with patch("classify_media.stream_chat", return_value={"tags": ["trees"], "reason": "Trees"}) as call:
            self.assertEqual(cli.infer(["image"], ["trees"], "vision:test", "http://gpu", 10)["tags"], ["trees"])
            payload = call.call_args.args[0]
            self.assertTrue(payload["stream"])
            self.assertEqual(set(payload["format"]["properties"]), {"tags", "reason"})
            self.assertEqual(payload["messages"][0]["images"], ["image"])
            self.assertNotIn("sample.jpg", payload["messages"][0]["content"])

    def test_even_media_sampling(self):
        self.assertEqual(cli.sample_media(list(range(10)), 3), [0, 4, 9])
        self.assertEqual(cli.sample_media(list(range(10)), 0), list(range(10)))

    def test_special_category_is_excluded_from_analysis_and_limit(self):
        self.db.execute("INSERT INTO categories VALUES(0,'Uncategorized','[\"Keep root\"]',NULL)")
        self.db.execute("INSERT INTO videos SELECT 2,'unused-root-path',root,'Uncategorized',media_type,0,mtime,size_bytes FROM videos WHERE id=1")
        self.db.commit()
        code, records = self.run_cli(["--apply", "--limit", "1"])
        self.assertEqual(code, 0)
        self.assertEqual([record["category"] for record in records], ["Nature"])
        code, records = self.run_cli(["--apply", "--source-category", "UNCATEGORIZED"], [])
        self.assertEqual((code, records), (0, []))
        self.assertEqual(self.db.execute("SELECT tags FROM categories WHERE id=0").fetchone()[0], '["Keep root"]')

    def test_special_category_in_old_report_is_skipped(self):
        path = self.saved_preview()
        record = json.loads(path.read_text())
        self.db.execute("UPDATE categories SET name='Uncategorized' WHERE id=1")
        self.db.commit()
        record["category"] = "Uncategorized"
        for entry in record["evidence"]:
            entry.pop("snapshot")
            entry.pop("identity")
        path.write_text(json.dumps(record) + "\n")
        code, progress, raw = self.replay(path)
        self.assertEqual(code, 0)
        self.assertFalse(json.loads(raw)["applied"])
        self.assertIn("special category", progress)
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])
        with self.assertRaises(ValueError):
            cli.apply_result(self.db, {"id": 1, "name": "uncategorized"}, [], ["trees"])

    def saved_preview(self, legacy=False):
        _, records = self.run_cli(["--source-category", "Nature"])
        if legacy:
            for entry in records[0]["evidence"]:
                entry.pop("snapshot")
                entry.pop("identity")
        path = self.root / "preview.jsonl"
        path.write_text("\n".join(json.dumps(record) for record in records) + "\n")
        return path

    def replay(self, path, extra=()):
        output, errors = io.StringIO(), io.StringIO()
        with patch("classify_media.infer", side_effect=AssertionError("LLM must not run")), patch("classify_media.previews", side_effect=AssertionError("Frames must not run")), contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            code = cli.main(["--db", str(self.db_path), "--apply-report", str(path), *extra])
        return code, output.getvalue(), errors.getvalue()

    def test_apply_saved_preview_without_model_merges_latest_and_is_idempotent(self):
        path = self.saved_preview()
        self.db.execute("UPDATE categories SET tags='[\"Concurrent\",\"Art, Music\"]' WHERE id=1")
        self.db.commit()
        code, progress, raw = self.replay(path)
        self.assertEqual(code, 0)
        self.assertIn("no LLM calls", progress)
        self.assertIn("[1/1]", progress)
        self.assertIn('Proposed tags for \'Nature\': ["trees"]', progress)
        record = json.loads(raw)
        self.assertEqual(record["added_tags"], ["trees"])
        self.assertEqual(self.stored(), ["Concurrent", "Art, Music", "trees"])
        self.assertEqual(dict(self.db.execute("SELECT * FROM videos").fetchone()), dict(self.item))
        self.assertEqual(self.replay(path)[0], 0)
        self.assertEqual(self.stored(), ["Concurrent", "Art, Music", "trees"])

    def test_old_preview_remains_usable(self):
        path = self.saved_preview(legacy=True)
        code, progress, _ = self.replay(path)
        self.assertEqual(code, 0)
        self.assertIn("Older report", progress)
        self.assertEqual(self.stored(), ["Existing", "Art, Music", "trees"])

    def test_saved_preview_rejects_changed_media(self):
        path = self.saved_preview()
        self.path.write_bytes(b"changed")
        code, _, raw = self.replay(path)
        self.assertEqual(code, 1)
        self.assertFalse(json.loads(raw)["applied"])
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])

    def test_saved_preview_rejects_renamed_category(self):
        path = self.saved_preview()
        self.db.execute("UPDATE categories SET name='Renamed' WHERE id=1")
        self.db.commit()
        self.assertEqual(self.replay(path)[0], 1)
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])

    def test_report_fully_validated_before_any_writes(self):
        path = self.saved_preview()
        valid = path.read_text()
        for invalid in ('{bad json}', valid.strip(), '{"category_id":2,"category":"Empty","suggested_tags":[null]}'):
            path.write_text(valid + invalid + "\n")
            code, _, error = self.replay(path)
            self.assertEqual(code, 1)
            self.assertIn("Report line 2", error)
            self.assertEqual(self.stored(), ["Existing", "Art, Music"])

    def test_saved_preview_with_errors_is_skipped(self):
        path = self.saved_preview()
        record = json.loads(path.read_text())
        record["errors"] = [{"error": "offline"}]
        path.write_text(json.dumps(record) + "\n")
        code, _, raw = self.replay(path)
        self.assertEqual(code, 1)
        self.assertFalse(json.loads(raw)["applied"])
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])

    def test_cannot_overwrite_input_report(self):
        path = self.saved_preview()
        before = path.read_bytes()
        self.assertEqual(self.replay(path, ["--report", str(path)])[0], 1)
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])

    def test_inference_progress_stdout_and_valid_jsonl_report(self):
        output, errors = io.StringIO(), io.StringIO()
        report = self.root / "progress-preview.jsonl"
        with patch("classify_media.previews", return_value=["preview"]), patch("classify_media.infer", return_value={"tags": ["trees"], "reason": "Trees"}), contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            code = cli.main(["--db", str(self.db_path), "--tags", str(self.tag_path), "--model", "vision:test", "--report", str(report)])
        self.assertEqual(code, 0)
        self.assertIn("[1/2] Category 'Nature'", output.getvalue())
        self.assertIn("Media 1: extracting previews", output.getvalue())
        self.assertIn("waiting for Ollama", output.getvalue())
        self.assertIn("Finished", output.getvalue())
        self.assertIn('Proposed tags for \'Nature\': ["trees"]', output.getvalue())
        self.assertIn("Proposed tags for 'Empty': []", output.getvalue())
        self.assertEqual(errors.getvalue(), "")
        records = [json.loads(line) for line in report.read_text().splitlines()]
        self.assertEqual(len(records), 2)
        self.assertIn("snapshot", records[0]["evidence"][0])
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])

    def test_optional_tag_list_restricts_saved_suggestions(self):
        path = self.saved_preview()
        labels = self.root / "restricted.json"
        labels.write_text('["other"]')
        self.assertEqual(self.replay(path, ["--tags", str(labels)])[0], 1)
        self.assertEqual(self.stored(), ["Existing", "Art, Music"])

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
