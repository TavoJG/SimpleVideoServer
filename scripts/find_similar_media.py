#!/usr/bin/env python3
"""Report perceptually similar media pairs. Never deletes files. Requires FFmpeg."""

import argparse
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import sys

from dedupe_media import candidates, signature

IMAGES = set('.jpg .jpeg .png .gif .webp .bmp .tif .tiff .avif'.split())


def command(args):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=60, check=False)
    if result.returncode:
        raise ValueError(result.stderr.decode(errors='replace').strip()[-1000:])
    return result.stdout


def dhash(pixels):
    if len(pixels) != 72:
        raise ValueError('Could not decode a complete frame')
    value = 0
    for row in range(8):
        for col in range(8):
            offset = row * 9 + col
            value = (value << 1) | (pixels[offset] > pixels[offset + 1])
    return value


def frame_hash(path, seconds=None):
    args = ['ffmpeg', '-v', 'error', '-nostdin', '-threads', '1']
    if seconds is not None:
        args += ['-ss', str(seconds)]
    args += ['-i', str(path), '-map', '0:v:0', '-frames:v', '1',
             '-vf', 'scale=9:8:flags=area,format=gray', '-threads', '1',
             '-f', 'rawvideo', '-pix_fmt', 'gray', 'pipe:1']
    return dhash(command(args))


def fingerprint(path, samples):
    before = signature(path.lstat())
    if path.suffix.lower() in IMAGES:
        result = {'kind': 'image', 'duration': None, 'hashes': [frame_hash(path)]}
    else:
        metadata = json.loads(command(['ffprobe', '-v', 'error', '-show_entries',
                                       'format=duration', '-of', 'json', str(path)]))
        duration = float(metadata['format']['duration'])
        if not math.isfinite(duration) or duration <= 0:
            raise ValueError('Video duration must be positive and finite')
        result = {'kind': 'video', 'duration': duration,
                  'hashes': [frame_hash(path, duration * (i + 1) / (samples + 1))
                             for i in range(samples)]}
    if signature(path.lstat()) != before:
        raise ValueError('File changed during fingerprinting')
    return dict(result, path=str(path))


def compare(left, right, threshold, duration_tolerance):
    if left['kind'] != right['kind']:
        return None
    if left['kind'] == 'video':
        gap = abs(left['duration'] - right['duration']) / max(left['duration'], right['duration'])
        if gap > duration_tolerance:
            return None
    distances = [(a ^ b).bit_count() for a, b in zip(left['hashes'], right['hashes'])]
    # Every sampled frame must match; an average could hide differing footage.
    if max(distances) > threshold:
        return None
    return {'left': left['path'], 'right': right['path'], 'kind': left['kind'],
            'similarity_percent': round(100 * (1 - sum(distances) / (64 * len(distances))), 2),
            'max_hash_distance': max(distances), 'frame_distances': distances}


def run(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('root', nargs='?', default=os.getenv('VIDEO_ROOT'))
    parser.add_argument('--recursive', action='store_true')
    parser.add_argument('--threshold', type=int, default=6,
                        help='Maximum differing bits per 64-bit frame hash (default: 6)')
    parser.add_argument('--samples', type=int, default=5, help='Video samples (default: 5)')
    parser.add_argument('--duration-tolerance', type=float, default=0.02,
                        help='Maximum relative video duration difference (default: 0.02)')
    parser.add_argument('--json', action='store_true', help='Print a JSON report')
    args = parser.parse_args(argv)
    if not args.root:
        parser.error('provide a media folder or set VIDEO_ROOT')
    if not 0 <= args.threshold <= 64 or not 1 <= args.samples <= 100:
        parser.error('threshold must be 0..64 and samples must be 1..100')
    if not 0 <= args.duration_tolerance <= 1:
        parser.error('duration-tolerance must be 0..1')
    if not all(shutil.which(tool) for tool in ('ffmpeg', 'ffprobe')):
        parser.error('install ffmpeg and ffprobe first')
    root = Path(args.root).expanduser().resolve(strict=True)
    if not root.is_dir():
        parser.error('root must be a directory')
    fingerprints, errors = [], []
    for path in sorted(candidates(root, args.recursive)):
        print(f'Fingerprinting {str(path)!r}', file=sys.stderr)
        try:
            fingerprints.append(fingerprint(path, args.samples))
        except (OSError, ValueError, KeyError, subprocess.TimeoutExpired) as exc:
            errors.append({'path': str(path), 'error': str(exc)})
            print(f'Skipped {str(path)!r}: {exc}', file=sys.stderr)
    matches = []
    for index, left in enumerate(fingerprints):
        for right in fingerprints[index + 1:]:
            match = compare(left, right, args.threshold, args.duration_tolerance)
            if match is not None:
                matches.append(match)
    matches.sort(key=lambda item: (-item['similarity_percent'], item['left'], item['right']))
    if args.json:
        print(json.dumps({'scanned': len(fingerprints), 'matches': matches, 'errors': errors}, indent=2))
    else:
        for match in matches:
            print(f"{match['similarity_percent']:.2f}% hash similarity ({match['kind']})\n"
                  f"  {match['left']!r}\n  {match['right']!r}")
        print(f'{len(fingerprints)} files scanned; {len(matches)} candidate pairs; {len(errors)} errors.')
        print('Review candidates manually. Hash similarity is not a probability of duplication.')
    return 1 if errors else 0


if __name__ == '__main__':
    try:
        sys.exit(run())
    except (OSError, ValueError) as exc:
        print(f'Error: {exc}', file=sys.stderr)
        sys.exit(1)
