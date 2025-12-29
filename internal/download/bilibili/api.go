package bilibili

import (
	"EasyDownload/internal/infra/logger"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// GetVideoInfo fetches video metadata from Bilibili.
// This is a convenience method that returns video info with stream data for
// the first part only (for backward compatibility with single-part videos).
// For multi-part videos, use GetVideoInfoWithParts and GetPartStreams.
//
// API: GET https://api.bilibili.com/x/web-interface/view
func (bd *BilibiliDownloader) GetVideoInfo(bvid string) (*BilibiliVideo, error) {
	video, err := bd.GetVideoInfoWithParts(bvid)
	if err != nil {
		return nil, err
	}

	// Get stream info for the first part
	if len(video.Parts) > 0 {
		aid := int64(0)
		fmt.Sscanf(video.AV, "av%d", &aid)
		streams, err := bd.getStreamInfo(bvid, aid, video.Parts[0].CID, video.Parts[0].Duration)
		if err == nil {
			video.Streams = streams
		}
	}

	return video, nil
}

// GetVideoInfoWithParts fetches video metadata including all parts (分P) from Bilibili.
// Returns a BilibiliVideo containing the video's metadata and list of parts.
// Stream information for each part is NOT included; call GetPartStreams or
// GetAllPartsStreams to fetch stream URLs when needed.
//
// API: GET https://api.bilibili.com/x/web-interface/view?bvid={bvid}
func (bd *BilibiliDownloader) GetVideoInfoWithParts(bvid string) (*BilibiliVideo, error) {
	// API endpoint
	apiURL := fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?bvid=%s", bvid)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			BVID     string `json:"bvid"`
			AID      int64  `json:"aid"`
			Title    string `json:"title"`
			Pic      string `json:"pic"`
			Desc     string `json:"desc"`
			Duration int    `json:"duration"`
			Owner    struct {
				Name string `json:"name"`
			} `json:"owner"`
			CID   int64 `json:"cid"`
			Pages []struct {
				CID      int64  `json:"cid"`
				Page     int    `json:"page"`
				Part     string `json:"part"`
				Duration int    `json:"duration"`
			} `json:"pages"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("API error: %s", result.Message)
	}

	video := &BilibiliVideo{
		BV:       result.Data.BVID,
		AV:       fmt.Sprintf("av%d", result.Data.AID),
		Title:    result.Data.Title,
		Cover:    result.Data.Pic,
		Author:   result.Data.Owner.Name,
		Duration: result.Data.Duration,
		Desc:     result.Data.Desc,
		Parts:    make([]BilibiliPart, 0, len(result.Data.Pages)),
	}

	// Parse parts (分P)
	if len(result.Data.Pages) > 0 {
		for _, page := range result.Data.Pages {
			part := BilibiliPart{
				CID:      page.CID,
				Page:     page.Page,
				PartName: page.Part,
				Duration: page.Duration,
			}
			video.Parts = append(video.Parts, part)
		}
	} else {
		// Single part video - create a default part
		video.Parts = append(video.Parts, BilibiliPart{
			CID:      result.Data.CID,
			Page:     1,
			PartName: result.Data.Title,
			Duration: result.Data.Duration,
		})
	}

	return video, nil
}

// getStreamInfo fetches available stream URLs and qualities for a video part.
// This is an internal method that calls the Bilibili playurl API.
//
// API: GET https://api.bilibili.com/x/player/playurl
//
// Request Parameters:
//   - bvid: Video BV ID (e.g., "BV1xx411c7mD")
//   - cid:  Content ID for the specific part
//   - fnval: Video stream format flags (bitwise OR):
//   - 1:    MP4 format (legacy)
//   - 16:   DASH format (modern, separate video/audio streams)
//   - 64:   Require HDR video
//   - 128:  Require 4K video
//   - 256:  Require Dolby Audio
//   - 512:  Require Dolby Vision
//   - 1024: Require 8K video
//   - 2048: Require AV1 codec
//     Value 4048 = 16|64|128|256|512|1024|2048 (request all high-quality formats)
//   - fnver: Format version, always 0
//   - fourk: Enable 4K quality (1=enable, 0=disable)
//
// Quality (qn) values returned in response:
//   - 127: 8K Ultra HD
//   - 126: Dolby Vision
//   - 125: HDR True Color
//   - 120: 4K Ultra HD (requires VIP)
//   - 116: 1080P60 High Frame Rate (requires VIP)
//   - 112: 1080P+ High Bitrate (requires VIP)
//   - 80:  1080P Full HD
//   - 74:  720P60 High Frame Rate
//   - 64:  720P HD
//   - 32:  480P Standard Definition
//   - 16:  360P Low Definition
func (bd *BilibiliDownloader) getStreamInfo(bvid string, _ int64, cid int64, duration int) ([]BilibiliStream, error) {
	// Play URL API - fnval=4048 requests DASH format with all quality options
	apiURL := fmt.Sprintf("https://api.bilibili.com/x/player/playurl?bvid=%s&cid=%d&fnval=4048&fnver=0&fourk=1", bvid, cid)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code int `json:"code"`
		Data struct {
			Quality       int      `json:"quality"`
			AcceptQuality []int    `json:"accept_quality"`
			AcceptDesc    []string `json:"accept_description"`
			Dash          struct {
				Video []struct {
					ID        int    `json:"id"`
					BaseURL   string `json:"baseUrl"`
					Bandwidth int64  `json:"bandwidth"`
					Codecs    string `json:"codecs"`
				} `json:"video"`
				Audio []struct {
					ID        int    `json:"id"`
					BaseURL   string `json:"baseUrl"`
					Bandwidth int64  `json:"bandwidth"`
				} `json:"audio"`
			} `json:"dash"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("failed to get stream info")
	}

	qualityNames := map[int]string{
		127: "8K",
		126: "杜比视界",
		125: "HDR",
		120: "4K",
		116: "1080P60",
		112: "1080P+",
		80:  "1080P",
		74:  "720P60",
		64:  "720P",
		32:  "480P",
		16:  "360P",
	}

	var streams []BilibiliStream

	// Get best audio bandwidth for size estimation
	var audioBandwidth int64
	if len(result.Data.Dash.Audio) > 0 {
		audioBandwidth = result.Data.Dash.Audio[0].Bandwidth
	}

	// Create stream entries for each quality
	for i, q := range result.Data.AcceptQuality {
		stream := BilibiliStream{
			Quality:     q,
			QualityName: qualityNames[q],
			Format:      "dash",
		}

		if i < len(result.Data.AcceptDesc) {
			stream.QualityName = result.Data.AcceptDesc[i]
		}

		// Find matching video stream
		var videoBandwidth int64
		for _, v := range result.Data.Dash.Video {
			if v.ID == q {
				stream.VideoURL = v.BaseURL
				videoBandwidth = v.Bandwidth
				break
			}
		}

		// Get first audio stream
		if len(result.Data.Dash.Audio) > 0 {
			stream.AudioURL = result.Data.Dash.Audio[0].BaseURL
		}

		// Estimate file size: (video_bandwidth + audio_bandwidth) * duration / 8
		// bandwidth is in bits per second, duration is in seconds
		if duration > 0 && videoBandwidth > 0 {
			stream.Size = (videoBandwidth + audioBandwidth) * int64(duration) / 8
		}

		if stream.VideoURL != "" {
			streams = append(streams, stream)
		}
	}

	return streams, nil
}

// GetPartStreams fetches available stream qualities for a specific video part.
// The partIndex is 0-based (0 for first part, 1 for second, etc.).
// Returns a list of available stream qualities with their URLs and estimated sizes.
func (bd *BilibiliDownloader) GetPartStreams(video *BilibiliVideo, partIndex int) ([]BilibiliStream, error) {
	if partIndex < 0 || partIndex >= len(video.Parts) {
		return nil, fmt.Errorf("invalid part index: %d (total parts: %d)", partIndex, len(video.Parts))
	}

	aid := int64(0)
	fmt.Sscanf(video.AV, "av%d", &aid)

	return bd.getStreamInfo(video.BV, aid, video.Parts[partIndex].CID, video.Parts[partIndex].Duration)
}

// GetAllPartsStreams fetches stream information for all parts of a video concurrently.
// This populates the Streams field for each BilibiliPart in the video.
// Useful for displaying size estimates in a part selector UI before downloading.
// Uses up to 4 concurrent API requests to speed up fetching.
func (bd *BilibiliDownloader) GetAllPartsStreams(video *BilibiliVideo) error {
	if video == nil || len(video.Parts) == 0 {
		return fmt.Errorf("no parts available")
	}

	aid := int64(0)
	fmt.Sscanf(video.AV, "av%d", &aid)

	// Parallel fetch with concurrency limit
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 4) // Limit to 4 concurrent requests

	for i := range video.Parts {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			streams, err := bd.getStreamInfo(video.BV, aid, video.Parts[i].CID, video.Parts[i].Duration)
			if err != nil {
				logger.Debug("Failed to get streams for part %d: %v", i, err)
				return
			}

			mu.Lock()
			video.Parts[i].Streams = streams
			mu.Unlock()
		}()
	}

	wg.Wait()
	return nil
}
