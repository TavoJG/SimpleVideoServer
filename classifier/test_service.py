import json
import io
import os
import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer
from unittest.mock import patch

from service import Handler, classify, validate_result


class ClassifierTests(unittest.TestCase):
    def test_result_validation_and_abstention(self):
        result = validate_result(dict(subcategory=" forest ", is_new=True, abstain=False, reason="Trees"), ["Forest"])
        self.assertEqual(result["subcategory"], "Forest")
        self.assertFalse(result["is_new"])
        result = validate_result(dict(subcategory="", is_new=True, abstain=True, reason="Ambiguous"), [])
        self.assertFalse(result["is_new"])
        for name in ("../escape", "", "..", "a\0b"):
            with self.assertRaises(ValueError):
                validate_result(dict(subcategory=name, is_new=True, abstain=False, reason=""), [])
        with self.assertRaises(ValueError):
            validate_result({"subcategory": "Forest"}, [])

    def test_auth_and_inference_unavailable(self):
        with patch.dict(os.environ, CLASSIFIER_TOKEN="secret", CLASSIFIER_MODEL="test"):
            server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            url = f"http://127.0.0.1:{server.server_port}/classify"
            try:
                with self.assertRaises(urllib.error.HTTPError) as error:
                    urllib.request.urlopen(urllib.request.Request(url, data=b"{}"))
                self.assertEqual(error.exception.code, 401)
                request = urllib.request.Request(url, data=b"{}", headers={"Authorization": "Bearer secret"})
                with patch("service.classify", side_effect=OSError("offline")):
                    with self.assertRaises(urllib.error.HTTPError) as error:
                        urllib.request.urlopen(request)
                self.assertEqual(error.exception.code, 503)
            finally:
                server.shutdown()
                server.server_close()
                worker.join()

    def test_malformed_input(self):
        with self.assertRaises(ValueError):
            classify({"parent_category": "Nature", "images": ["not-base64"]})

    def test_visual_request_and_structured_response(self):
        import base64
        payload = {"parent_category": "Nature", "existing_subcategories": [],
                   "images": [base64.b64encode(b"\xff\xd8preview").decode()]}
        response = {"message": {"content": json.dumps(dict(subcategory="Forests", is_new=True, abstain=False, reason="Trees"))}}
        with patch.dict(os.environ, CLASSIFIER_MODEL="vision:test"):
            with patch("service.urllib.request.urlopen", return_value=io.BytesIO(json.dumps(response).encode())) as invoke:
                result = classify(payload)
                request = json.loads(invoke.call_args.args[0].data)
                self.assertEqual(request["messages"][0]["images"], payload["images"])
                self.assertEqual(request["model"], "vision:test")
                self.assertTrue(result["is_new"])
            malformed = {"message": {"content": "not json"}}
            with patch("service.urllib.request.urlopen", return_value=io.BytesIO(json.dumps(malformed).encode())):
                with self.assertRaises(ValueError):
                    classify(payload)


if __name__ == "__main__":
    unittest.main()
