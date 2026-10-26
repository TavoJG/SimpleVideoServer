import contextlib
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import find_similar_media as similar


class SimilarityTests(unittest.TestCase):
    def test_hash_and_filters(self):
        self.assertEqual(similar.dhash(bytes(range(72))), 0)
        self.assertEqual(similar.dhash(bytes(reversed(range(72)))), (1 << 64) - 1)
        left = dict(kind='video', duration=10, hashes=[0, 0, 0], path='a')
        right = dict(kind='video', duration=10.1, hashes=[0, 0, 127], path='b')
        self.assertIsNone(similar.compare(left, right, 6, .02))
        right['hashes'] = [0, 1, 0]
        self.assertIsNotNone(similar.compare(left, right, 6, .02))
        right['duration'] = 12
        self.assertIsNone(similar.compare(left, right, 6, .02))
        right['kind'] = 'image'
        self.assertIsNone(similar.compare(left, right, 6, .02))

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'FFmpeg required')
    def test_reencoded_media_and_invalid_file(self):
        def ffmpeg(*args):
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-y', *args], check=True)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            original = root / 'original.mp4'
            copy = root / 'resized.mp4'
            ffmpeg('-f', 'lavfi', '-i', 'testsrc2=size=160x120:rate=10', '-t', '2',
                   '-c:v', 'mpeg4', '-threads', '1', str(original))
            ffmpeg('-i', str(original), '-vf', 'scale=80:60', '-c:v', 'mpeg4',
                   '-q:v', '5', '-threads', '1', str(copy))
            self.assertNotEqual(hashlib.sha256(original.read_bytes()).digest(),
                                hashlib.sha256(copy.read_bytes()).digest())
            a = similar.fingerprint(original, 3)
            b = similar.fingerprint(copy, 3)
            self.assertIsNotNone(similar.compare(a, b, 6, .02))
            ffmpeg('-i', str(original), '-frames:v', '1', '-threads', '1', str(root / 'a.png'))
            ffmpeg('-i', str(root / 'a.png'), '-vf', 'scale=80:60', '-threads', '1', str(root / 'b.jpg'))
            self.assertIsNotNone(similar.compare(similar.fingerprint(root / 'a.png', 3),
                                               similar.fingerprint(root / 'b.jpg', 3), 6, .02))
            (root / 'bad.mp4').write_bytes(b'invalid')
            with contextlib.redirect_stdout(io.StringIO()) as out, contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(similar.run([tmp, '--json', '--samples', '3']), 1)
            report = json.loads(out.getvalue())
            self.assertEqual(report['scanned'], 4)
            self.assertEqual(len(report['matches']), 2)
            self.assertEqual(len(report['errors']), 1)
            self.assertEqual(len(list(root.iterdir())), 5)


if __name__ == '__main__':
    unittest.main()
