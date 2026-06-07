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

// codecPriority returns the priority of a codec for selection.
// Lower number = higher priority. AV1 (13) > HEVC (12) > H.264 (7).
// Unknown codecs get priority 99 (lowest).
func codecPriority(codecid int) int {
	switch codecid {
	case 13: // AV1
		return 0
	case 12: // HEVC
		return 1
	case 7: // H.264/AVC
		return 2
	default:
		return 99
	}
}

// dashVideoEntry holds a single DASH video stream entry from the API response.
// Fields use both camelCase and snake_case JSON tags to handle API response variance.
type dashVideoEntry struct {
	ID         int    `json:"id"`
	BaseURL    string `json:"baseUrl"`
	Bandwidth  int64  `json:"bandwidth"`
	Codecs     string `json:"codecs"`
	CodecID    int    `json:"codecid"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FrameRate  string `json:"frameRate"`
	MimeType   string `json:"mimeType"`
	BackupURL1 string `json:"-"`
	BackupURL2 string `json:"-"`
}

// dashAudioEntry holds a single DASH audio stream entry from the API response.
type dashAudioEntry struct {
	ID         int    `json:"id"`
	BaseURL    string `json:"baseUrl"`
	Bandwidth  int64  `json:"bandwidth"`
	Codecs     string `json:"codecs"`
	BackupURL1 string `json:"-"`
	BackupURL2 string `json:"-"`
}

// supportFormatEntry holds a quality format entry from support_formats.
type supportFormatEntry struct {
	Quality        int    `json:"quality"`
	Format         string `json:"format"`
	NewDescription string `json:"new_description"`
	DisplayDesc    string `json:"display_desc"`
	Superscript    string `json:"superscript"`
}

// rawDashVideo is the raw DASH video entry with both camelCase and snake_case backup URL fields.
type rawDashVideo struct {
	ID             int      `json:"id"`
	BaseURL        string   `json:"baseUrl"`
	BaseURLSnake   string   `json:"base_url"`
	BackupURL      []string `json:"backupUrl"`
	BackupURLSnake []string `json:"backup_url"`
	Bandwidth      int64    `json:"bandwidth"`
	Codecs         string   `json:"codecs"`
	CodecID        int      `json:"codecid"`
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	FrameRate      string   `json:"frameRate"`
	FrameRateSnake string   `json:"frame_rate"`
	MimeType       string   `json:"mimeType"`
	MimeTypeSnake  string   `json:"mime_type"`
}

// rawDashAudio is the raw DASH audio entry with both camelCase and snake_case backup URL fields.
type rawDashAudio struct {
	ID             int      `json:"id"`
	BaseURL        string   `json:"baseUrl"`
	BaseURLSnake   string   `json:"base_url"`
	BackupURL      []string `json:"backupUrl"`
	BackupURLSnake []string `json:"backup_url"`
	Bandwidth      int64    `json:"bandwidth"`
	Codecs         string   `json:"codecs"`
}

// selectURLs picks the primary URL from the two possible field names (camelCase priority),
// merges backup URL arrays from both field names, de-duplicates URLs, and promotes
// the first backup URL when no primary URL is provided.
func selectURLs(baseURL, baseURLSnake string, backupURL, backupURLSnake []string) (primary string, backups []string) {
	backupCandidates := make([]string, 0, len(backupURL)+len(backupURLSnake))
	backupCandidates = append(backupCandidates, backupURL...)
	backupCandidates = append(backupCandidates, backupURLSnake...)

	urls := collectDownloadURLs(selectString(baseURL, baseURLSnake), backupCandidates)
	if len(urls) == 0 {
		return "", nil
	}
	return urls[0], urls[1:]
}

// selectString picks the first non-empty string from the given candidates.
func selectString(candidates ...string) string {
	for _, s := range candidates {
		if s != "" {
			return s
		}
	}
	return ""
}

// selectBestVideoStream picks the best DASH video stream for a given quality.
// Priority: AV1 > HEVC > H.264, then higher bandwidth within the same codec family.
func selectBestVideoStream(candidates []rawDashVideo) *rawDashVideo {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		c := &candidates[i]
		pBest := codecPriority(best.CodecID)
		pCurr := codecPriority(c.CodecID)
		if pCurr < pBest || (pCurr == pBest && c.Bandwidth > best.Bandwidth) {
			best = c
		}
	}
	return best
}

// selectBestAudio picks the DASH audio stream with the highest bandwidth.
func selectBestAudio(candidates []rawDashAudio) *rawDashAudio {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		if candidates[i].Bandwidth > best.Bandwidth {
			best = &candidates[i]
		}
	}
	return best
}

// qualityNameMap is a fallback mapping when support_formats is unavailable.
var qualityNameMap = map[int]string{
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

// buildQualityName resolves a quality name from support_formats first, then API/fallback maps.
func buildQualityName(q int, supportFormats []supportFormatEntry, acceptDesc string) string {
	for _, sf := range supportFormats {
		if sf.Quality == q {
			name := selectString(sf.NewDescription, sf.DisplayDesc, sf.Format)
			if sf.Superscript != "" {
				if name != "" {
					name += " " + sf.Superscript
				} else {
					name = sf.Superscript
				}
			}
			if name != "" {
				return name
			}
			break
		}
	}
	if acceptDesc != "" {
		return acceptDesc
	}
	if name, ok := qualityNameMap[q]; ok {
		return name
	}
	return fmt.Sprintf("未知(%d)", q)
}

// getStreamInfo fetches available stream URLs and qualities for a video part.
// It selects the best codec per quality (AV1 > HEVC > H.264), the highest-bandwidth
// audio track, and collects backup CDN URLs for resilience.
//
// API: GET https://api.bilibili.com/x/player/playurl
func (bd *BilibiliDownloader) getStreamInfo(bvid string, _ int64, cid int64, duration int) ([]BilibiliStream, error) {
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
			Quality        int                  `json:"quality"`
			AcceptQuality  []int                `json:"accept_quality"`
			AcceptDesc     []string             `json:"accept_description"`
			SupportFormats []supportFormatEntry `json:"support_formats"`
			Dash           struct {
				Video []rawDashVideo `json:"video"`
				Audio []rawDashAudio `json:"audio"`
			} `json:"dash"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("failed to get stream info")
	}

	// Select best audio track (highest bandwidth)
	bestAudio := selectBestAudio(result.Data.Dash.Audio)
	var audioBandwidth int64
	var audioURL string
	var audioBackupURLs []string
	if bestAudio != nil {
		audioBandwidth = bestAudio.Bandwidth
		audioURL, audioBackupURLs = selectURLs(
			bestAudio.BaseURL, bestAudio.BaseURLSnake,
			bestAudio.BackupURL, bestAudio.BackupURLSnake,
		)
	}

	// Build support_formats lookup for quality names
	supportFormats := result.Data.SupportFormats

	var streams []BilibiliStream

	for i, q := range result.Data.AcceptQuality {
		acceptDesc := ""
		if i < len(result.Data.AcceptDesc) {
			acceptDesc = result.Data.AcceptDesc[i]
		}

		// Collect all video streams for this quality across codecs
		var candidates []rawDashVideo
		for _, v := range result.Data.Dash.Video {
			if v.ID == q {
				candidates = append(candidates, v)
			}
		}
		if len(candidates) == 0 {
			continue
		}

		best := selectBestVideoStream(candidates)
		if best == nil {
			continue
		}

		videoURL, videoBackupURLs := selectURLs(
			best.BaseURL, best.BaseURLSnake,
			best.BackupURL, best.BackupURLSnake,
		)
		if videoURL == "" {
			continue
		}

		frameRate := selectString(best.FrameRate, best.FrameRateSnake)
		mimeType := selectString(best.MimeType, best.MimeTypeSnake)

		stream := BilibiliStream{
			Quality:         q,
			QualityName:     buildQualityName(q, supportFormats, acceptDesc),
			Format:          "dash",
			VideoURL:        videoURL,
			AudioURL:        audioURL,
			Width:           best.Width,
			Height:          best.Height,
			FrameRate:       frameRate,
			Codecs:          best.Codecs,
			CodecID:         best.CodecID,
			MimeType:        mimeType,
			BackupURLs:      videoBackupURLs,
			AudioBackupURLs: audioBackupURLs,
		}

		// Estimate file size: (video_bandwidth + audio_bandwidth) * duration / 8
		if duration > 0 && best.Bandwidth > 0 {
			stream.Size = (best.Bandwidth + audioBandwidth) * int64(duration) / 8
		}

		streams = append(streams, stream)
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
