import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading
import time
import unittest
from unittest.mock import patch

import classify_media as cli


class StreamHandler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        scenario = payload.get('scenario', 'success')
        self.server.last_path = self.path
        self.server.last_token = self.headers.get('Authorization')

        def write(value):
            self.wfile.write(json.dumps(value).encode() + b'\n')
            self.wfile.flush()

        try:
            if scenario == 'headers_stall':
                time.sleep(0.3)
            self.send_response(503 if scenario == 'http_error' else 200)
            self.send_header('Content-Type', 'application/x-ndjson')
            self.end_headers()
            if scenario == 'http_error':
                self.wfile.write(b'unavailable')
                return
            if scenario == 'stream_error':
                write({'error': 'GPU unavailable'})
                return
            if scenario == 'invalid_json':
                self.wfile.write(b'not json\n')
                return
            if scenario == 'oversized':
                write({'message': {'content': 'x' * 1_000_001}, 'done': False})
                return
            if scenario == 'never_finishes':
                for _ in range(30):
                    write({'message': {'content': ' '}, 'done': False})
                    time.sleep(0.02)
                return
            write({'message': {'content': '{"tags":'}, 'done': False})
            time.sleep(0.3 if scenario == 'body_stall' else 0.04)
            write({'message': {'content': '["trees"],"reason":"Trees"}'}, 'done': False})
            if scenario != 'premature':
                write({'done': True, 'eval_count': 12})
        except (BrokenPipeError, ConnectionResetError):
            pass


class StreamingTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(('127.0.0.1', 0), StreamHandler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.url = f'http://127.0.0.1:{cls.server.server_port}/proxy'

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def request(self, scenario, **kwargs):
        return cli.stream_chat({'scenario': scenario}, self.url, kwargs.pop('timeout', 2), **kwargs)

    def test_stream_assembles_valid_json_and_emits_waiting_updates(self):
        updates = []
        result = self.request('success', token='secret', progress=updates.append, progress_interval=0.01)
        self.assertEqual(result, {'tags': ['trees'], 'reason': 'Trees'})
        self.assertEqual(self.server.last_path, '/proxy/api/chat')
        self.assertEqual(self.server.last_token, 'Bearer secret')
        self.assertTrue(any('first response chunk' in update for update in updates))
        self.assertTrue(any('elapsed' in update for update in updates))
        self.assertTrue(any('response complete' in update for update in updates))
        self.assertFalse(any(thread.name == 'ollama-progress' for thread in threading.enumerate()))

    def test_header_stall_has_heartbeat_and_read_timeout(self):
        updates = []
        with self.assertRaisesRegex(TimeoutError, 'idle-read timeout'):
            self.request('headers_stall', read_timeout=0.08, progress_interval=0.01, progress=updates.append)
        self.assertTrue(any('waiting for first response chunk' in update for update in updates))

    def test_body_stall_times_out_without_accepting_partial_tags(self):
        with self.assertRaisesRegex(TimeoutError, 'idle-read timeout'):
            self.request('body_stall', read_timeout=0.05)

    def test_overall_deadline_stops_even_an_active_stream(self):
        started = time.monotonic()
        with self.assertRaisesRegex(TimeoutError, 'overall deadline'):
            self.request('never_finishes', timeout=0.15, read_timeout=1)
        self.assertLess(time.monotonic() - started, 0.7)

    def test_invalid_interrupted_and_failed_responses(self):
        for scenario, message in [('premature', 'completion marker'), ('stream_error', 'GPU unavailable'),
                                  ('http_error', 'HTTP 503'), ('invalid_json', None), ('oversized', 'exceeds limit')]:
            with self.subTest(scenario=scenario):
                with self.assertRaises(ValueError) as error:
                    self.request(scenario)
                if message:
                    self.assertIn(message, str(error.exception))

    def test_connection_timeout_is_distinct(self):
        with patch.object(http.client.HTTPConnection, 'connect', side_effect=TimeoutError('socket timeout')):
            with self.assertRaisesRegex(TimeoutError, 'connection timeout'):
                self.request('success', connect_timeout=0.1)

    def test_quiet_transport_emits_no_progress(self):
        self.assertEqual(self.request('success')['tags'], ['trees'])
