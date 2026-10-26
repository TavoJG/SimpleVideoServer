package main

import "testing"

func TestPlaybackLevel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream playbackStream
		status string
	}{
		{"confirmed iPhone failure", playbackStream{"h264", 30, 718, 1280, "30/1"}, "warning"},
		{"corrected level", playbackStream{"h264", 31, 718, 1280, "30/1"}, "ok"},
		{"excessive frame rate", playbackStream{"h264", 31, 1280, 720, "60/1"}, "warning"},
		{"valid SD", playbackStream{"h264", 30, 640, 480, "30000/1001"}, "ok"},
		{"unknown rate", playbackStream{"h264", 30, 718, 1280, "0/0"}, "unknown"},
		{"other codec", playbackStream{"hevc", 30, 718, 1280, "30/1"}, "unknown"},
		{"unknown level", playbackStream{"h264", -99, 718, 1280, "30/1"}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkPlaybackLevel(tc.stream); got.Status != tc.status {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestPlaybackCheckRoutes(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	lockedID := libraryID(t, s, "secret.jpg")
	if got := libraryCall(t, s, "GET", "/api/videos/"+fmtID(lockedID)+"/playback-check", nil); got.Code != 403 {
		t.Fatalf("locked category status %d", got.Code)
	}
	if got := libraryCall(t, s, "GET", "/api/videos/"+fmtID(id)+"/playback-check", nil); got.Code != 400 {
		t.Fatalf("image status %d: %s", got.Code, got.Body.String())
	}
	if got := libraryCall(t, s, "POST", "/api/videos/"+fmtID(id)+"/playback-check", nil); got.Code != 405 {
		t.Fatalf("method status %d", got.Code)
	}
	if got := libraryCall(t, s, "GET", "/api/videos/999999/playback-check", nil); got.Code != 404 {
		t.Fatalf("missing status %d", got.Code)
	}
}
