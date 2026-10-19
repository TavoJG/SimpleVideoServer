import contextlib
import io
import json
from pathlib import Path
import sqlite3
import unittest
from unittest.mock import patch

import classify_media as cli
import test_classify_media as fixtures


class CheckpointTests(unittest.TestCase):
    setUp = fixtures.CategoryTaggerTests.setUp
    stored = fixtures.CategoryTaggerTests.stored

    def run_cached(self, extra=(), results=None, interrupt=False):
        output = io.StringIO()
        results = results if results is not None else [{"tags": ["trees"], "reason": "Trees"}]
        with patch("classify_media.previews", return_value=["preview"]) as previews, patch("classify_media.infer", side_effect=results) as infer, contextlib.redirect_stdout(output):
            args = ["--db", str(self.db_path), "--tags", str(self.tag_path), "--model", "vision:test", "--quiet", *extra]
            if interrupt:
                with self.assertRaises(KeyboardInterrupt):
                    cli.main(args)
                code = 130
            else:
                code = cli.main(args)
        return code, [json.loads(line) for line in output.getvalue().splitlines()], infer.call_count, previews.call_count

    def second_media(self):
        second = self.root / "second.jpg"
        second.write_bytes(b"sample")
        self.db.execute("INSERT INTO videos SELECT 2,?,root,category,media_type,0,?,size_bytes FROM videos WHERE id=1", (str(second), second.stat().st_mtime))
        self.db.commit()

    def test_preview_then_apply_reuses_without_llm_and_merges_current_tags(self):
        self.assertEqual(self.run_cached()[2], 1)
        self.db.execute("UPDATE categories SET tags='[\"Manual\"]' WHERE id=1")
        self.db.commit()
        code, records, requests, extraction = self.run_cached(["--apply"], [])
        self.assertEqual((code, requests, extraction), (0, 0, 0))
        self.assertEqual(records[0]["cached_media"], 1)
        self.assertEqual(self.stored(), ["Manual", "trees"])
        self.assertEqual(self.run_cached(["--apply"], [])[2], 0)
        self.assertEqual(self.stored(), ["Manual", "trees"])

    def test_partial_failure_retries_only_failed_media(self):
        self.second_media()
        good = {"tags": ["trees"], "reason": "Trees"}
        self.assertEqual(self.run_cached(results=[good, TimeoutError("timed out")])[0], 1)
        code, records, requests, extraction = self.run_cached(["--apply"], [good])
        self.assertEqual((code, requests, extraction), (0, 1, 1))
        self.assertEqual(records[0]["cached_media"], 1)
        self.assertEqual(records[0]["analyzed_media"], 1)
        self.assertEqual(len(records[0]["evidence"]), 2)

    def test_interrupt_inside_category_preserves_completed_items(self):
        self.second_media()
        good = {"tags": ["trees"], "reason": "Trees"}
        self.run_cached(results=[good, KeyboardInterrupt()], interrupt=True)
        code, records, requests, _ = self.run_cached(results=[good])
        self.assertEqual((code, requests), (0, 1))
        self.assertEqual(records[0]["cached_media"], 1)

    def test_new_config_invalidates_checkpoint(self):
        self.run_cached()
        for extra in (["--model", "vision:other"], ["--frames", "1"], ["--ollama-url", "http://other-host:11434"]):
            with self.subTest(extra=extra):
                self.assertEqual(self.run_cached(extra)[2], 1)
        self.tag_path.write_text('["trees", "art, music", "other"]')
        self.assertEqual(self.run_cached()[2], 1)

    def test_file_change_invalidates_checkpoint_after_scan(self):
        self.run_cached()
        self.path.write_bytes(b"changed contents")
        self.db.execute("UPDATE videos SET size_bytes=?,mtime=? WHERE id=1", (self.path.stat().st_size, self.path.stat().st_mtime))
        self.db.commit()
        self.assertEqual(self.run_cached()[2], 1)

    def test_stale_database_and_changed_during_inference_are_not_cached(self):
        self.path.write_bytes(b"stale")
        self.assertEqual(self.run_cached()[0], 1)
        cache_path = Path(str(self.db_path) + '.classifier-checkpoint.sqlite3')
        with contextlib.closing(sqlite3.connect(cache_path)) as cache:
            self.assertEqual(cache.execute("SELECT COUNT(*) FROM media_results").fetchone()[0], 0)
        self.path.write_bytes(b"sample")
        self.db.execute("UPDATE videos SET mtime=?", (self.path.stat().st_mtime,))
        self.db.commit()
        def changed(*args, **kwargs):
            self.path.write_bytes(b"changed during model call")
            return {"tags": ["trees"], "reason": "Trees"}
        with patch("classify_media.previews", return_value=["preview"]), patch("classify_media.infer", side_effect=changed), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(cli.main(["--db", str(self.db_path), "--tags", str(self.tag_path), "--model", "vision:test", "--quiet"]), 1)
        with contextlib.closing(sqlite3.connect(cache_path)) as cache:
            self.assertEqual(cache.execute("SELECT COUNT(*) FROM media_results").fetchone()[0], 0)

    def test_apply_failure_can_retry_without_new_inference(self):
        self.db.executescript("CREATE TRIGGER reject_tags BEFORE UPDATE ON categories BEGIN SELECT RAISE(ABORT,'test'); END;")
        self.assertEqual(self.run_cached(["--apply"])[0], 1)
        self.db.execute("DROP TRIGGER reject_tags")
        self.db.commit()
        self.assertEqual(self.run_cached(["--apply"], [])[2], 0)
        self.assertIn("trees", self.stored())

    def test_no_checkpoint_forces_fresh_work(self):
        self.run_cached()
        self.assertEqual(self.run_cached(["--no-checkpoint"])[2], 1)

    def test_checkpoint_path_cannot_replace_inputs_or_reports(self):
        for path in (self.db_path, self.tag_path, self.path):
            with self.subTest(path=path), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(self.run_cached(["--checkpoint", str(path)])[0], 1)
        target = self.root / 'report.jsonl'
        target.write_text('keep')
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(self.run_cached(["--checkpoint", str(target), "--report", str(target)])[0], 1)
        self.assertEqual(target.read_text(), 'keep')

    def test_custom_checkpoint_and_resume_progress(self):
        path = self.root / 'resume.sqlite3'
        self.run_cached(["--checkpoint", str(path)])
        report = self.root / 'report.jsonl'
        output = io.StringIO()
        with patch("classify_media.infer", side_effect=AssertionError("should reuse")), patch("classify_media.previews", side_effect=AssertionError("should reuse")), contextlib.redirect_stdout(output):
            code = cli.main(["--db", str(self.db_path), "--tags", str(self.tag_path), "--model", "vision:test", "--checkpoint", str(path), "--report", str(report)])
        self.assertEqual(code, 0)
        self.assertIn("reused checkpoint; no Ollama call", output.getvalue())
        self.assertIn("0 analyzed, 1 reused", output.getvalue())


if __name__ == '__main__':
    unittest.main()
