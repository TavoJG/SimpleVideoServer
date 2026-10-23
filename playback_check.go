package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type playbackStream struct {
	Codec  string `json:"codec_name"`
	Level  int    `json:"level"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Rate   string `json:"r_frame_rate"`
}

type playbackCheck struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Only check necessary frame-size and processing-rate limits. A passing result
// does not validate bitrate, reference frames, audio, file integrity or delivery.
func checkPlaybackLevel(v playbackStream) playbackCheck {
	unknown := playbackCheck{"unknown", "Could not assess H.264 level requirements for this video."}
	if v.Codec != "h264" {
		return playbackCheck{"unknown", "This check detects H.264 level mismatches only. This video uses a different codec."}
	}
	parts := strings.Split(v.Rate, "/")
	if len(parts) != 2 {
		return unknown
	}
	numerator, e1 := strconv.ParseFloat(parts[0], 64)
	denominator, e2 := strconv.ParseFloat(parts[1], 64)
	if e1 != nil || e2 != nil || numerator <= 0 || denominator <= 0 || v.Width <= 0 || v.Height <= 0 || v.Level <= 0 {
		return unknown
	}
	fps := numerator / denominator
	if math.IsNaN(fps) || math.IsInf(fps, 0) {
		return unknown
	}
	width := math.Ceil(float64(v.Width) / 16)
	height := math.Ceil(float64(v.Height) / 16)
	blocks := width * height
	limits := []struct {
		level       int
		frame, rate float64
	}{
		{10, 99, 1485}, {11, 396, 3000}, {12, 396, 6000}, {13, 396, 11880},
		{20, 396, 11880}, {21, 792, 19800}, {22, 1620, 20250}, {30, 1620, 40500},
		{31, 3600, 108000}, {32, 5120, 216000}, {40, 8192, 245760}, {41, 8192, 245760},
		{42, 8704, 522240}, {50, 22080, 589824}, {51, 36864, 983040}, {52, 36864, 2073600},
		{60, 139264, 4177920}, {61, 139264, 8355840}, {62, 139264, 16711680},
	}
	for _, limit := range limits {
		if limit.level != v.Level {
			continue
		}
		if blocks > limit.frame || blocks*fps > limit.rate+0.01 || width*width > 8*limit.frame || height*height > 8*limit.frame {
			return playbackCheck{"warning", fmt.Sprintf("Possible iPhone playback issue: this video declares H.264 Level %.1f, but %d × %d at %.2f fps exceeds that level's frame-size or processing limits. The level metadata may need repair.", float64(v.Level)/10, v.Width, v.Height, fps)}
		}
		return playbackCheck{"ok", "No H.264 level mismatch detected for resolution and frame rate. This does not guarantee iPhone playback; other encoding, file or delivery issues may still exist."}
	}
	return unknown
}

func (s *server) videoPlaybackCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	item, err := s.queryVideo(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.requireCategoryAccess(w, r, item.Category) {
		return
	}
	if item.MediaType != "video" {
		writeError(w, 400, "Select a video to check playback compatibility.")
		return
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		writeError(w, 503, "Compatibility checking requires ffprobe on the server. Install FFmpeg and try again.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, probe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,level,width,height,r_frame_rate", "-of", "json", "-i", item.Path).Output()
	if err != nil {
		writeError(w, 422, "Could not inspect this video. The file may be unavailable, damaged, or took too long to inspect.")
		return
	}
	var result struct {
		Streams []playbackStream `json:"streams"`
	}
	if json.Unmarshal(output, &result) != nil || len(result.Streams) == 0 {
		writeError(w, 422, "No readable video stream found.")
		return
	}
	writeJSON(w, 200, checkPlaybackLevel(result.Streams[0]))
}
