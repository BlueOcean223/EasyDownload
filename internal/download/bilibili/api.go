package bilibili

import (
	"EasyDownload/internal/infra/logger"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
		streams, err := bd.getStreamInfo(video.BV, aid, video.Parts[0].CID, video.Parts[0].Duration)
		if err == nil {
			video.Streams = streams
		}
	}

	return video, nil
}

func buildVideoViewAPIURL(videoID string) string {
	query := url.Values{}
	if strings.HasPrefix(strings.ToLower(videoID), "av") {
		query.Set("aid", strings.TrimPrefix(strings.ToLower(videoID), "av"))
	} else {
		query.Set("bvid", videoID)
	}
	return "https://api.bilibili.com/x/web-interface/view?" + query.Encode()
}

// GetVideoInfoWithParts fetches video metadata including all parts (分P) from Bilibili.
// Returns a BilibiliVideo containing the video's metadata and list of parts.
// Stream information for each part is NOT included; call GetPartStreams or
// GetAllPartsStreams to fetch stream URLs when needed.
//
// API: GET https://api.bilibili.com/x/web-interface/view?bvid={bvid}
func (bd *BilibiliDownloader) GetVideoInfoWithParts(bvid string) (*BilibiliVideo, error) {
	// API endpoint
	apiURL := buildVideoViewAPIURL(bvid)

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
		Streams:  []BilibiliStream{},
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

const defaultBangumiQuality = 127

type bangumiSeasonAPIResponse struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Result  bangumiSeasonResult `json:"result"`
}

type bangumiSeasonResult struct {
	SeasonID       int              `json:"season_id"`
	SeasonTitle    string           `json:"season_title"`
	MediaID        int              `json:"media_id"`
	Title          string           `json:"title"`
	Cover          string           `json:"cover"`
	Evaluate       string           `json:"evaluate"`
	SeasonType     *int             `json:"season_type"`
	ShowSeasonType int              `json:"show_season_type"`
	Episodes       []bangumiEpisode `json:"episodes"`
}

type bangumiEpisode struct {
	AID                int64  `json:"aid"`
	BVID               string `json:"bvid"`
	CID                int64  `json:"cid"`
	EpID               int64  `json:"ep_id"`
	ID                 int64  `json:"id"`
	Title              string `json:"title"`
	LongTitle          string `json:"long_title"`
	ShowTitle          string `json:"show_title"`
	Badge              string `json:"badge"`
	BadgeType          int    `json:"badge_type"`
	Status             int    `json:"status"`
	Duration           int    `json:"duration"`
	Cover              string `json:"cover"`
	SectionType        int    `json:"section_type"`
	ShowDRMLoginDialog bool   `json:"showDrmLoginDialog"`
}

// GetBangumiInfo fetches PGC/bangumi metadata by episode ID. The season API
// returns all episodes in the season; Streams is populated for the requested
// episode only.
func (bd *BilibiliDownloader) GetBangumiInfo(epID string) (*BilibiliVideo, error) {
	return bd.GetBangumiInfoByID("ep", epID)
}

// GetBangumiInfoByID fetches PGC/bangumi metadata by ep, season, or media ID.
// For season/media links where no current episode is encoded in the URL, the
// first playable formal episode is used as the current episode.
func (bd *BilibiliDownloader) GetBangumiInfoByID(kind, id string) (*BilibiliVideo, error) {
	if id == "" {
		return nil, fmt.Errorf("empty bangumi id")
	}

	params := url.Values{}
	var requestedEPID int64

	switch kind {
	case "ep":
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid bangumi ep id: %s", id)
		}
		requestedEPID = parsed
		params.Set("ep_id", id)
	case "season", "ss":
		params.Set("season_id", id)
	case "media", "md":
		params.Set("media_id", id)
	default:
		return nil, fmt.Errorf("unsupported bangumi id kind: %s", kind)
	}

	season, err := bd.fetchBangumiSeason(params)
	if err != nil && (kind == "media" || kind == "md") {
		seasonID, resolveErr := bd.resolveBangumiMediaSeasonID(id)
		if resolveErr == nil && seasonID > 0 {
			fallbackParams := url.Values{}
			fallbackParams.Set("season_id", strconv.Itoa(seasonID))
			season, err = bd.fetchBangumiSeason(fallbackParams)
		} else if resolveErr != nil {
			logger.Debug("Failed to resolve bangumi media id %s: %v", id, resolveErr)
		}
	}
	if err != nil {
		return nil, err
	}

	return bd.bangumiSeasonToVideo(season, requestedEPID), nil
}

func (bd *BilibiliDownloader) fetchBangumiSeason(params url.Values) (*bangumiSeasonResult, error) {
	apiURL := "https://api.bilibili.com/pgc/view/web/season?" + params.Encode()

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

	var result bangumiSeasonAPIResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("bangumi season API error: %s", result.Message)
	}
	if len(result.Result.Episodes) == 0 {
		return nil, fmt.Errorf("bangumi season has no episodes")
	}
	return &result.Result, nil
}

func (bd *BilibiliDownloader) resolveBangumiMediaSeasonID(mediaID string) (int, error) {
	apiURL := "https://api.bilibili.com/pgc/review/user?media_id=" + url.QueryEscape(mediaID)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return 0, err
	}
	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Result  struct {
			SeasonID int `json:"season_id"`
			Media    struct {
				SeasonID int `json:"season_id"`
			} `json:"media"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	if result.Code != 0 {
		return 0, fmt.Errorf("bangumi media API error: %s", result.Message)
	}
	if result.Result.Media.SeasonID > 0 {
		return result.Result.Media.SeasonID, nil
	}
	if result.Result.SeasonID > 0 {
		return result.Result.SeasonID, nil
	}
	return 0, fmt.Errorf("season_id not found for media_id=%s", mediaID)
}

func (bd *BilibiliDownloader) bangumiSeasonToVideo(season *bangumiSeasonResult, requestedEPID int64) *BilibiliVideo {
	currentIndex := selectCurrentBangumiEpisode(season.Episodes, requestedEPID)
	current := season.Episodes[currentIndex]

	parts := make([]BilibiliPart, 0, len(season.Episodes))
	for i, ep := range season.Episodes {
		part := bangumiEpisodeToPart(ep, i)
		parts = append(parts, part)
	}

	seasonType := 0
	if season.SeasonType != nil {
		seasonType = *season.SeasonType
	}
	if seasonType == 0 {
		seasonType = season.ShowSeasonType
	}

	currentPart := parts[currentIndex]
	video := &BilibiliVideo{
		BV:               current.BVID,
		AV:               formatAID(current.AID),
		Title:            selectString(season.SeasonTitle, season.Title),
		Cover:            selectString(season.Cover, current.Cover),
		Author:           "Bilibili 番剧",
		Duration:         currentPart.Duration,
		Desc:             season.Evaluate,
		Parts:            parts,
		Streams:          []BilibiliStream{},
		SeasonID:         season.SeasonID,
		MediaID:          season.MediaID,
		EpID:             currentPart.EpID,
		Badge:            currentPart.Badge,
		SeasonType:       seasonType,
		IsBangumi:        true,
		TotalEps:         len(parts),
		CurrentPartIndex: currentIndex,
	}

	if current.BVID != "" && current.CID > 0 {
		streams, err := bd.getBangumiStreamInfo(current.BVID, current.CID, defaultBangumiQuality, currentPart.Duration)
		if err != nil {
			logger.Debug("Failed to get bangumi streams for ep %d: %v", currentPart.EpID, err)
		} else {
			video.Streams = streams
			video.Parts[currentIndex].Streams = streams
		}
	}

	return video
}

func selectCurrentBangumiEpisode(episodes []bangumiEpisode, requestedEPID int64) int {
	if len(episodes) == 0 {
		return 0
	}
	if requestedEPID > 0 {
		for i, ep := range episodes {
			if ep.EpID == requestedEPID || ep.ID == requestedEPID {
				return i
			}
		}
	}
	for i, ep := range episodes {
		if ep.SectionType == 0 && ep.Status == 2 {
			return i
		}
	}
	for i, ep := range episodes {
		if ep.SectionType == 0 {
			return i
		}
	}
	for i, ep := range episodes {
		if ep.Status == 2 {
			return i
		}
	}
	return 0
}

func bangumiEpisodeToPart(ep bangumiEpisode, index int) BilibiliPart {
	return BilibiliPart{
		CID:         ep.CID,
		Page:        index + 1,
		PartName:    bangumiEpisodeTitle(ep, index),
		Duration:    bangumiDurationSeconds(ep.Duration),
		BV:          ep.BVID,
		AID:         ep.AID,
		EpID:        firstNonZeroInt64(ep.EpID, ep.ID),
		Badge:       ep.Badge,
		BadgeType:   ep.BadgeType,
		SectionType: ep.SectionType,
		Cover:       ep.Cover,
	}
}

func bangumiEpisodeTitle(ep bangumiEpisode, index int) string {
	if ep.ShowTitle != "" {
		return ep.ShowTitle
	}
	if ep.Title != "" && ep.LongTitle != "" {
		return fmt.Sprintf("第%s话 %s", ep.Title, ep.LongTitle)
	}
	return selectString(ep.LongTitle, ep.Title, fmt.Sprintf("第%d集", index+1))
}

func bangumiDurationSeconds(durationMS int) int {
	if durationMS <= 0 {
		return 0
	}
	seconds := durationMS / 1000
	if seconds == 0 {
		return durationMS
	}
	return seconds
}

func formatAID(aid int64) string {
	if aid <= 0 {
		return ""
	}
	return fmt.Sprintf("av%d", aid)
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// GetBangumiPlayInfo fetches PGC/bangumi stream information using the dedicated
// pgc/player/web/playurl endpoint. The ordinary x/player/playurl endpoint often
// returns empty DASH data for PGC content.
func (bd *BilibiliDownloader) GetBangumiPlayInfo(bvid string, cid int64, quality int) ([]BilibiliStream, error) {
	return bd.getBangumiStreamInfo(bvid, cid, quality, 0)
}

func (bd *BilibiliDownloader) getBangumiStreamInfo(bvid string, cid int64, quality int, duration int) ([]BilibiliStream, error) {
	if quality <= 0 {
		quality = defaultBangumiQuality
	}

	query := url.Values{}
	query.Set("bvid", bvid)
	query.Set("cid", strconv.FormatInt(cid, 10))
	query.Set("qn", strconv.Itoa(quality))
	query.Set("fnval", "4048")
	query.Set("fnver", "0")
	query.Set("fourk", "1")
	query.Set("drm_tech_type", "2")
	query.Set("otype", "json")
	query.Set("platform", "web")
	apiURL := "https://api.bilibili.com/pgc/player/web/playurl?" + query.Encode()

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
		Code    int         `json:"code"`
		Message string      `json:"message"`
		Data    playURLData `json:"data"`
		Result  playURLData `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("failed to get bangumi stream info: %s", result.Message)
	}

	data := result.Data
	if len(data.Dash.Video) == 0 && len(result.Result.Dash.Video) > 0 {
		data = result.Result
	}
	if len(data.Dash.Video) == 0 {
		return nil, fmt.Errorf("no playable bangumi DASH streams; login/VIP may be required or this episode is unavailable")
	}

	streams := streamsFromPlayURLData(data, duration)
	if len(streams) == 0 {
		return nil, fmt.Errorf("no usable bangumi streams found")
	}
	return streams, nil
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
	BiliDRMURI     string   `json:"bilidrm_uri"`
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

// playURLData is the common subset returned by both ordinary video playurl and
// PGC/bangumi playurl APIs. Ordinary videos usually include support_formats,
// while PGC responses may only include accept_description.
type playURLData struct {
	Quality        int                  `json:"quality"`
	AcceptQuality  []int                `json:"accept_quality"`
	AcceptDesc     []string             `json:"accept_description"`
	SupportFormats []supportFormatEntry `json:"support_formats"`
	DRMTechType    int                  `json:"drm_tech_type"`
	Dash           struct {
		Video []rawDashVideo `json:"video"`
		Audio []rawDashAudio `json:"audio"`
	} `json:"dash"`
}

var biliDRMKIDRegex = regexp.MustCompile(`(?i)(?:bili://|[?&]kid=)([0-9a-f]{32})`)

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

func qualitiesFromPlayURLData(data playURLData) []int {
	if len(data.AcceptQuality) > 0 {
		return data.AcceptQuality
	}

	qualities := make([]int, 0)
	seen := make(map[int]struct{})
	for _, v := range data.Dash.Video {
		if _, ok := seen[v.ID]; ok {
			continue
		}
		seen[v.ID] = struct{}{}
		qualities = append(qualities, v.ID)
	}
	return qualities
}

func extractBiliDRMKID(uri, streamURL string) string {
	for _, candidate := range []string{uri, streamURL} {
		if matches := biliDRMKIDRegex.FindStringSubmatch(candidate); len(matches) > 1 {
			return matches[1]
		}
	}
	return ""
}

func streamsFromPlayURLData(data playURLData, duration int) []BilibiliStream {
	// Select best audio track (highest bandwidth)
	bestAudio := selectBestAudio(data.Dash.Audio)
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

	supportFormats := data.SupportFormats
	qualities := qualitiesFromPlayURLData(data)
	streams := make([]BilibiliStream, 0, len(qualities))

	for i, q := range qualities {
		acceptDesc := ""
		if i < len(data.AcceptDesc) {
			acceptDesc = data.AcceptDesc[i]
		}

		// Collect all video streams for this quality across codecs
		var candidates []rawDashVideo
		for _, v := range data.Dash.Video {
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
		drmTechType := data.DRMTechType
		if drmTechType == 0 && best.BiliDRMURI != "" {
			drmTechType = 2
		}

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
			DRMTechType:     drmTechType,
			KID:             extractBiliDRMKID(best.BiliDRMURI, videoURL),
			BiliDRMURI:      best.BiliDRMURI,
		}

		// Estimate file size: (video_bandwidth + audio_bandwidth) * duration / 8
		if duration > 0 && best.Bandwidth > 0 {
			stream.Size = (best.Bandwidth + audioBandwidth) * int64(duration) / 8
		}

		streams = append(streams, stream)
	}

	return streams
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
		Code    int         `json:"code"`
		Message string      `json:"message"`
		Data    playURLData `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("failed to get stream info: %s", result.Message)
	}

	return streamsFromPlayURLData(result.Data, duration), nil
}

// GetPartStreams fetches available stream qualities for a specific video part.
// The partIndex is 0-based (0 for first part, 1 for second, etc.).
// Returns a list of available stream qualities with their URLs and estimated sizes.
func (bd *BilibiliDownloader) GetPartStreams(video *BilibiliVideo, partIndex int) ([]BilibiliStream, error) {
	if partIndex < 0 || partIndex >= len(video.Parts) {
		return nil, fmt.Errorf("invalid part index: %d (total parts: %d)", partIndex, len(video.Parts))
	}

	part := video.Parts[partIndex]
	if video.IsBangumi {
		bvid := selectString(part.BV, video.BV)
		if bvid == "" {
			return nil, fmt.Errorf("bangumi episode missing bvid")
		}
		return bd.getBangumiStreamInfo(bvid, part.CID, defaultBangumiQuality, part.Duration)
	}

	aid := int64(0)
	fmt.Sscanf(video.AV, "av%d", &aid)

	return bd.getStreamInfo(video.BV, aid, part.CID, part.Duration)
}

// GetAllPartsStreams fetches stream information for all parts of a video concurrently.
// This populates the Streams field for each BilibiliPart in the video.
// Useful for displaying size estimates in a part selector UI before downloading.
// Uses up to 4 concurrent API requests to speed up fetching.
func (bd *BilibiliDownloader) GetAllPartsStreams(video *BilibiliVideo) error {
	if video == nil || len(video.Parts) == 0 {
		return fmt.Errorf("no parts available")
	}

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

			streams, err := bd.GetPartStreams(video, i)
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
