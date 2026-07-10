// Package fetch centralizes reliable file/media transfer behavior shared by
// platform adapters. It deliberately does not model platform JSON APIs and it
// never publishes a final artifact: callers receive a verified temporary file.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultNoProgressTimeout = 2 * time.Minute

type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type Fetcher interface {
	Download(ctx context.Context, request FetchRequest, destination Destination, progress ProgressReporter) (FetchResult, error)
	Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error)
}

type FetchRequest struct {
	URL                  string
	EquivalentMirrorURLs []string
	Headers              map[string]string
	Identity             ResourceIdentity
	ResumePolicy         ResumePolicy
	RetryPolicy          RetryPolicy
	MultipartPolicy      MultipartPolicy
	MaxBytes             int64
	// NoProgressTimeout bounds how long one HTTP attempt may go without
	// receiving response headers or body bytes. Zero uses
	// DefaultNoProgressTimeout.
	NoProgressTimeout time.Duration
}

// Request is retained as a source-compatible alias while callers migrate to
// the explicit FetchRequest contract.
type Request = FetchRequest

type ResourceIdentity struct {
	ExpectedSize int64
	ETag         string
	LastModified string
	SHA256       string
}

type ResourceValidator struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
}

type ProbeRequest struct {
	URL     string
	Headers map[string]string
	// NoProgressTimeout bounds the wait for probe response headers. Zero uses
	// DefaultNoProgressTimeout.
	NoProgressTimeout time.Duration
}

type Destination struct {
	TemporaryPath   string
	ResumeStatePath string
}

type ResumePolicy struct {
	Enabled                 bool
	RestartWhenRangeIgnored bool
	RequireRange            bool
}

type RetryPolicy struct {
	MaxAttempts          int
	InitialBackoff       time.Duration
	MaxBackoff           time.Duration
	RetryableStatusCodes []int
}

type MultipartPolicy struct {
	Enabled   bool
	PartSize  int64
	Threshold int64
}

type ProgressKind string

const (
	ProgressUpdate ProgressKind = "update"
	ProgressReset  ProgressKind = "reset"
)

type Progress struct {
	Downloaded int64
	Total      int64
	Kind       ProgressKind
}

type ProgressReporter func(Progress)

type FetchResult struct {
	TemporaryPath   string
	ResumeStatePath string
	Downloaded      int64
	Total           int64
	URL             string
	Attempts        int
	Resumed         bool
	SHA256          string
	Validator       ResourceValidator
}

// Result is retained as a source-compatible alias while callers migrate to
// the explicit FetchResult contract.
type Result = FetchResult

type ProbeResult struct {
	URL          string
	StatusCode   int
	ContentSize  int64
	AcceptRanges bool
	ContentType  string
	Validator    ResourceValidator
}

type ErrorKind string

const (
	ErrorStatusCode       ErrorKind = "status_code"
	ErrorRangeUnsupported ErrorKind = "range_unsupported"
	ErrorDestination      ErrorKind = "destination"
	ErrorNetwork          ErrorKind = "network"
	ErrorTimeout          ErrorKind = "timeout"
	ErrorCanceled         ErrorKind = "canceled"
	ErrorSizeLimit        ErrorKind = "size_limit"
	ErrorIntegrity        ErrorKind = "integrity"
	ErrorIdentityMismatch ErrorKind = "identity_mismatch"
	ErrorResumeState      ErrorKind = "resume_state"
)

type Error struct {
	Kind       ErrorKind
	URL        string
	StatusCode int
	Attempts   int
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("fetch %s failed: %s status=%d attempts=%d", e.URL, e.Kind, e.StatusCode, e.Attempts)
	}
	if e.Err != nil {
		return fmt.Sprintf("fetch %s failed: %s: %v", e.URL, e.Kind, e.Err)
	}
	return fmt.Sprintf("fetch %s failed: %s", e.URL, e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Client struct {
	doer     HTTPDoer
	sidecars sidecarStore
}

func New(doer HTTPDoer) *Client {
	if doer == nil {
		doer = &http.Client{Timeout: 0}
	}
	return &Client{doer: doer, sidecars: fileSidecarStore{}}
}

func (c *Client) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if request.NoProgressTimeout < 0 {
		return ProbeResult{}, errors.New("no-progress timeout must be non-negative")
	}
	requestContext, watchdog := withNoProgressWatchdog(ctx, effectiveNoProgressTimeout(request.NoProgressTimeout))
	defer watchdog.Stop()
	req, err := http.NewRequestWithContext(requestContext, http.MethodHead, request.URL, nil)
	if err != nil {
		return ProbeResult{}, err
	}
	applyTransferHeaders(req, request.Headers)
	resp, err := c.doer.Do(req)
	if err != nil {
		return ProbeResult{}, classifyDoError(requestContext, request.URL, err)
	}
	watchdog.Touch()
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return ProbeResult{}, &Error{Kind: ErrorStatusCode, URL: request.URL, StatusCode: resp.StatusCode}
	}
	return ProbeResult{
		URL:          request.URL,
		StatusCode:   resp.StatusCode,
		ContentSize:  resp.ContentLength,
		AcceptRanges: strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes"),
		ContentType:  resp.Header.Get("Content-Type"),
		Validator:    responseValidator(resp),
	}, nil
}

func (c *Client) Download(ctx context.Context, request Request, destination Destination, progress ProgressReporter) (Result, error) {
	resolved, err := resolveDestination(destination)
	if err != nil {
		return Result{}, err
	}
	if err := validateRequest(request, resolved); err != nil {
		return Result{}, err
	}
	urls := requestURLs(request)
	var lastErr error
	totalAttempts := 0
	for _, rawURL := range urls {
		if err := ctx.Err(); err != nil {
			return Result{}, classifyDoError(ctx, rawURL, err)
		}
		result, attempts, err := c.downloadURL(ctx, rawURL, urls, request, resolved, progress)
		totalAttempts += attempts
		if err == nil {
			result.Attempts = totalAttempts
			return result, nil
		}
		lastErr = withAttempts(err, totalAttempts)
		var fetchErr *Error
		if errors.As(err, &fetchErr) {
			if fetchErr.Kind == ErrorCanceled || fetchErr.Kind == ErrorDestination || fetchErr.Kind == ErrorSizeLimit {
				return Result{}, lastErr
			}
			if fetchErr.Kind == ErrorStatusCode && fetchErr.StatusCode == http.StatusTooManyRequests &&
				!statusExplicitlyRetryable(request.RetryPolicy, fetchErr.StatusCode) {
				return Result{}, lastErr
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no fetch URL provided")
	}
	return Result{}, lastErr
}

type resolvedDestination struct {
	temporaryPath string
	statePath     string
}

type resumeCandidate struct {
	state  *resumeState
	offset int64
}

func (c *Client) downloadURL(ctx context.Context, rawURL string, allURLs []string, request Request, destination resolvedDestination, progress ProgressReporter) (Result, int, error) {
	attempts := max(1, request.RetryPolicy.MaxAttempts)
	backoff := request.RetryPolicy.InitialBackoff
	if backoff <= 0 {
		backoff = 100 * time.Millisecond
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		var result Result
		var err error
		if request.MultipartPolicy.Enabled {
			result, err = c.downloadMultipartOnce(ctx, rawURL, request, destination, progress)
		} else {
			result, err = c.downloadOnce(ctx, rawURL, allURLs, request, destination, progress)
		}
		if err == nil {
			return result, attempt, nil
		}
		lastErr = err
		if !isRetryable(err, request.RetryPolicy) || attempt == attempts {
			return Result{}, attempt, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, attempt, classifyDoError(ctx, rawURL, ctx.Err())
		case <-timer.C:
		}
		backoff *= 2
		if request.RetryPolicy.MaxBackoff > 0 && backoff > request.RetryPolicy.MaxBackoff {
			backoff = request.RetryPolicy.MaxBackoff
		}
	}
	return Result{}, attempts, lastErr
}

func (c *Client) downloadOnce(ctx context.Context, rawURL string, allURLs []string, request Request, destination resolvedDestination, progress ProgressReporter) (Result, error) {
	for restart := 0; restart < 2; restart++ {
		candidate, err := c.prepareResume(request, destination, allURLs, progress)
		if err != nil {
			return Result{}, err
		}
		result, restartFresh, err := c.transferOnce(ctx, rawURL, request, destination, candidate, progress)
		if !restartFresh {
			return result, err
		}
		if err := c.resetResumeFiles(destination); err != nil {
			return Result{}, &Error{Kind: ErrorResumeState, URL: rawURL, Err: err}
		}
		reportReset(progress, expectedSizeValue(request))
	}
	return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, Err: errors.New("safe restart did not converge")}
}

func (c *Client) prepareResume(request Request, destination resolvedDestination, allURLs []string, progress ProgressReporter) (resumeCandidate, error) {
	info, statErr := os.Stat(destination.temporaryPath)
	if statErr != nil && !os.IsNotExist(statErr) {
		return resumeCandidate{}, &Error{Kind: ErrorDestination, Err: statErr}
	}
	if os.IsNotExist(statErr) {
		if err := c.sidecars.Remove(destination.statePath); err != nil {
			return resumeCandidate{}, &Error{Kind: ErrorResumeState, Err: err}
		}
		return resumeCandidate{}, nil
	}
	offset := info.Size()
	if !request.ResumePolicy.Enabled || offset <= 0 {
		if err := c.resetResumeFiles(destination); err != nil {
			return resumeCandidate{}, &Error{Kind: ErrorResumeState, Err: err}
		}
		if offset > 0 {
			reportReset(progress, expectedSizeValue(request))
		}
		return resumeCandidate{}, nil
	}
	state, err := c.sidecars.Load(destination.statePath)
	if err != nil || !resumeStateMatchesRequest(state, request, allURLs) || ifRangeValue(state) == "" {
		if resetErr := c.resetResumeFiles(destination); resetErr != nil {
			return resumeCandidate{}, &Error{Kind: ErrorResumeState, Err: resetErr}
		}
		reportReset(progress, expectedSizeValue(request))
		return resumeCandidate{}, nil
	}
	if state.TotalKnown && offset > state.Total {
		if err := c.resetResumeFiles(destination); err != nil {
			return resumeCandidate{}, &Error{Kind: ErrorResumeState, Err: err}
		}
		reportReset(progress, expectedSizeValue(request))
		return resumeCandidate{}, nil
	}
	return resumeCandidate{state: &state, offset: offset}, nil
}

func (c *Client) transferOnce(ctx context.Context, rawURL string, request Request, destination resolvedDestination, candidate resumeCandidate, progress ProgressReporter) (Result, bool, error) {
	requestContext, watchdog := withNoProgressWatchdog(ctx, effectiveNoProgressTimeout(request.NoProgressTimeout))
	defer watchdog.Stop()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, false, err
	}
	applyTransferHeaders(req, request.Headers)
	if candidate.offset > 0 && candidate.state != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", candidate.offset))
		req.Header.Set("If-Range", ifRangeValue(*candidate.state))
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return Result{}, false, classifyDoError(requestContext, rawURL, err)
	}
	watchdog.Touch()
	resp.Body = &activityReadCloser{ReadCloser: resp.Body, touch: watchdog.Touch}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if candidate.offset > 0 {
			if request.ResumePolicy.RequireRange || !request.ResumePolicy.RestartWhenRangeIgnored {
				return Result{}, false, &Error{Kind: ErrorRangeUnsupported, URL: rawURL, StatusCode: resp.StatusCode}
			}
			if err := c.resetResumeFiles(destination); err != nil {
				return Result{}, false, &Error{Kind: ErrorResumeState, URL: rawURL, Err: err}
			}
			reportReset(progress, expectedSizeValue(request))
		}
		result, err := c.writeFullResponse(requestContext, rawURL, request, destination, resp, progress)
		return result, false, err
	case http.StatusPartialContent:
		if candidate.offset <= 0 || candidate.state == nil {
			return Result{}, false, &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: errors.New("unexpected 206 without resumable partial")}
		}
		result, err := c.appendPartialResponse(requestContext, rawURL, request, destination, candidate, resp, progress)
		if err != nil && shouldSafeRestart(err, request.ResumePolicy) {
			return Result{}, true, nil
		}
		return result, false, err
	case http.StatusRequestedRangeNotSatisfiable:
		result, ok, err := c.completeFrom416(rawURL, request, destination, candidate, resp)
		if ok || err != nil && !shouldSafeRestart(err, request.ResumePolicy) {
			return result, false, err
		}
		return Result{}, true, nil
	default:
		return Result{}, false, &Error{Kind: ErrorStatusCode, URL: rawURL, StatusCode: resp.StatusCode}
	}
}

func (c *Client) writeFullResponse(ctx context.Context, rawURL string, request Request, destination resolvedDestination, resp *http.Response, progress ProgressReporter) (Result, error) {
	validator := responseValidator(resp)
	if err := validateExpectedValidator(request.Identity, validator); err != nil {
		return Result{}, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, Err: err}
	}
	total, totalKnown, err := responseTotal(request, resp.ContentLength)
	if err != nil {
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, Err: err}
	}
	if request.MaxBytes > 0 && totalKnown && total > request.MaxBytes {
		return Result{}, &Error{Kind: ErrorSizeLimit, URL: rawURL, Err: fmt.Errorf("total=%d limit=%d", total, request.MaxBytes)}
	}
	state := resumeState{
		Version:        resumeStateVersion,
		SelectedURL:    rawURL,
		ETag:           validator.ETag,
		LastModified:   validator.LastModified,
		Total:          total,
		TotalKnown:     totalKnown,
		ExpectedSHA256: requestSHA256(request),
	}
	if err := c.sidecars.Save(destination.statePath, state); err != nil {
		return Result{}, &Error{Kind: ErrorResumeState, URL: rawURL, Err: err}
	}
	file, err := os.OpenFile(destination.temporaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	copied, copyErr := copyWithProgress(ctx, file, resp.Body, 0, total, totalKnown, total, request.MaxBytes, progress)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		if isIntegrityError(copyErr) {
			_ = c.resetResumeFiles(destination)
		}
		return Result{}, addErrorURL(copyErr, rawURL)
	}
	if syncErr != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: syncErr}
	}
	if closeErr != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: closeErr}
	}
	if !totalKnown {
		total, totalKnown = copied, true
		state.Total, state.TotalKnown = total, true
		if err := c.sidecars.Save(destination.statePath, state); err != nil {
			return Result{}, &Error{Kind: ErrorResumeState, URL: rawURL, Err: err}
		}
	}
	return c.verifyCompleted(rawURL, request, destination, copied, total, false, validator)
}

func (c *Client) appendPartialResponse(ctx context.Context, rawURL string, request Request, destination resolvedDestination, candidate resumeCandidate, resp *http.Response, progress ProgressReporter) (Result, error) {
	contentRange, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil || contentRange.unsatisfied || !contentRange.totalKnown {
		if err == nil {
			err = errors.New("206 response has unsatisfied or unknown-total Content-Range")
		}
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: err}
	}
	if contentRange.start != candidate.offset {
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: fmt.Errorf("Content-Range starts at %d, partial size is %d", contentRange.start, candidate.offset)}
	}
	segmentLength := contentRange.end - contentRange.start + 1
	if resp.ContentLength >= 0 && resp.ContentLength != segmentLength {
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: fmt.Errorf("Content-Length=%d, Content-Range length=%d", resp.ContentLength, segmentLength)}
	}
	validator := responseValidator(resp)
	if !validatorMatchesState(*candidate.state, validator) {
		return Result{}, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, StatusCode: resp.StatusCode, Err: errors.New("206 validator is missing, downgraded, or changed")}
	}
	if err := validateExpectedValidator(request.Identity, validator); err != nil {
		return Result{}, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, Err: err}
	}
	expected, expectedKnown := expectedSize(request)
	if candidate.state.TotalKnown && candidate.state.Total != contentRange.total {
		return Result{}, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, Err: fmt.Errorf("resource total changed from %d to %d", candidate.state.Total, contentRange.total)}
	}
	if expectedKnown && expected != contentRange.total {
		return Result{}, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, Err: fmt.Errorf("Content-Range total=%d, expected=%d", contentRange.total, expected)}
	}
	if request.MaxBytes > 0 && contentRange.total > request.MaxBytes {
		return Result{}, &Error{Kind: ErrorSizeLimit, URL: rawURL, Err: fmt.Errorf("total=%d limit=%d", contentRange.total, request.MaxBytes)}
	}
	state := *candidate.state
	state.SelectedURL = rawURL
	state.Total = contentRange.total
	state.TotalKnown = true
	if err := c.sidecars.Save(destination.statePath, state); err != nil {
		return Result{}, &Error{Kind: ErrorResumeState, URL: rawURL, Err: err}
	}
	info, err := os.Stat(destination.temporaryPath)
	if err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	if info.Size() != candidate.offset {
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, Err: fmt.Errorf("partial size changed from %d to %d", candidate.offset, info.Size())}
	}
	file, err := os.OpenFile(destination.temporaryPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	copied, copyErr := copyWithProgress(ctx, file, resp.Body, candidate.offset, segmentLength, true, contentRange.total, request.MaxBytes, progress)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		if isIntegrityError(copyErr) {
			_ = c.resetResumeFiles(destination)
		}
		return Result{}, addErrorURL(copyErr, rawURL)
	}
	if syncErr != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: syncErr}
	}
	if closeErr != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: closeErr}
	}
	downloaded := candidate.offset + copied
	if downloaded < contentRange.total {
		return Result{}, &Error{Kind: ErrorNetwork, URL: rawURL, Err: io.ErrUnexpectedEOF}
	}
	if downloaded != contentRange.total {
		_ = c.resetResumeFiles(destination)
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, Err: fmt.Errorf("downloaded=%d total=%d", downloaded, contentRange.total)}
	}
	return c.verifyCompleted(rawURL, request, destination, downloaded, contentRange.total, true, validator)
}

func (c *Client) completeFrom416(rawURL string, request Request, destination resolvedDestination, candidate resumeCandidate, resp *http.Response) (Result, bool, error) {
	if candidate.offset <= 0 || candidate.state == nil {
		return Result{}, false, &Error{Kind: ErrorRangeUnsupported, URL: rawURL, StatusCode: resp.StatusCode, Err: errors.New("416 without resumable partial")}
	}
	contentRange, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil || !contentRange.unsatisfied || !contentRange.totalKnown {
		if err == nil {
			err = errors.New("416 response has invalid Content-Range")
		}
		return Result{}, false, &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: err}
	}
	validator := responseValidator(resp)
	if !validatorMatchesState(*candidate.state, validator) {
		return Result{}, false, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, StatusCode: resp.StatusCode, Err: errors.New("416 validator is missing, downgraded, or changed")}
	}
	expected, expectedKnown := expectedSize(request)
	if candidate.offset != contentRange.total ||
		candidate.state.TotalKnown && candidate.state.Total != contentRange.total ||
		expectedKnown && expected != contentRange.total {
		return Result{}, false, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, StatusCode: resp.StatusCode, Err: fmt.Errorf("416 total=%d partial=%d", contentRange.total, candidate.offset)}
	}
	result, err := c.verifyCompleted(rawURL, request, destination, candidate.offset, contentRange.total, true, validator)
	return result, err == nil, err
}

func (c *Client) verifyCompleted(rawURL string, request Request, destination resolvedDestination, downloaded, total int64, resumed bool, validator ResourceValidator) (Result, error) {
	if downloaded != total {
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, Err: fmt.Errorf("downloaded=%d total=%d", downloaded, total)}
	}
	digest, err := hashFile(destination.temporaryPath)
	if err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	if expected := requestSHA256(request); expected != "" && digest != expected {
		_ = c.resetResumeFiles(destination)
		return Result{}, &Error{Kind: ErrorIntegrity, URL: rawURL, Err: fmt.Errorf("sha256=%s expected=%s", digest, expected)}
	}
	return Result{
		TemporaryPath:   destination.temporaryPath,
		ResumeStatePath: destination.statePath,
		Downloaded:      downloaded,
		Total:           total,
		URL:             rawURL,
		Resumed:         resumed,
		SHA256:          digest,
		Validator:       validator,
	}, nil
}

func (c *Client) resetResumeFiles(destination resolvedDestination) error {
	if err := c.sidecars.Remove(destination.statePath); err != nil {
		return err
	}
	if err := os.Remove(destination.temporaryPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncParentDir(filepath.Dir(destination.temporaryPath))
}

func copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, offset, expected int64, expectedKnown bool, total int64, maxBytes int64, progress ProgressReporter) (int64, error) {
	buf := make([]byte, 32*1024)
	copied := int64(0)
	lastReported := int64(-1)
	for {
		if err := transferContextError(ctx); err != nil {
			return copied, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			next := copied + int64(n)
			if expectedKnown && next > expected {
				return copied, &Error{Kind: ErrorIntegrity, Err: fmt.Errorf("response body exceeds expected length %d", expected)}
			}
			if maxBytes > 0 && offset+next > maxBytes {
				return copied, &Error{Kind: ErrorSizeLimit, Err: fmt.Errorf("downloaded>%d", maxBytes)}
			}
			written, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				return copied, &Error{Kind: ErrorDestination, Err: writeErr}
			}
			if written != n {
				return copied, &Error{Kind: ErrorDestination, Err: io.ErrShortWrite}
			}
			copied = next
			if progress != nil && offset+copied != lastReported {
				lastReported = offset + copied
				progress(Progress{Downloaded: offset + copied, Total: total, Kind: ProgressUpdate})
			}
		}
		if readErr == io.EOF {
			if expectedKnown && copied < expected {
				return copied, &Error{Kind: ErrorNetwork, Err: io.ErrUnexpectedEOF}
			}
			return copied, nil
		}
		if readErr != nil {
			if err := transferContextError(ctx); err != nil {
				return copied, err
			}
			var networkError net.Error
			if errors.As(readErr, &networkError) && networkError.Timeout() {
				return copied, &Error{Kind: ErrorTimeout, Err: readErr}
			}
			return copied, &Error{Kind: ErrorNetwork, Err: readErr}
		}
	}
}

func resolveDestination(destination Destination) (resolvedDestination, error) {
	tempPath := strings.TrimSpace(destination.TemporaryPath)
	if tempPath == "" {
		return resolvedDestination{}, errors.New("temporary path is required")
	}
	statePath := strings.TrimSpace(destination.ResumeStatePath)
	if statePath == "" {
		statePath = tempPath + ".resume.json"
	}
	if filepath.Clean(tempPath) == filepath.Clean(statePath) {
		return resolvedDestination{}, errors.New("temporary path and resume state path must differ")
	}
	for _, path := range []string{tempPath, statePath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return resolvedDestination{}, &Error{Kind: ErrorDestination, Err: err}
		}
	}
	return resolvedDestination{temporaryPath: tempPath, statePath: statePath}, nil
}

func validateRequest(request Request, destination resolvedDestination) error {
	if len(requestURLs(request)) == 0 {
		return errors.New("fetch URL is required")
	}
	if destination.temporaryPath == "" {
		return errors.New("temporary path is required")
	}
	if request.Identity.ExpectedSize < 0 || request.MaxBytes < 0 {
		return errors.New("sizes must be non-negative")
	}
	if request.NoProgressTimeout < 0 {
		return errors.New("no-progress timeout must be non-negative")
	}
	if request.MultipartPolicy.Enabled {
		if request.MultipartPolicy.PartSize <= 0 {
			return errors.New("multipart part size must be positive")
		}
		if request.MultipartPolicy.Threshold < 0 {
			return errors.New("multipart threshold must be non-negative")
		}
	}
	if etag := strings.TrimSpace(request.Identity.ETag); etag != "" && normalizeStrongETag(etag) == "" {
		return errors.New("resource ETag must be strong")
	}
	if digest := strings.TrimSpace(request.Identity.SHA256); digest != "" {
		normalized := normalizeSHA256(digest)
		if len(normalized) != sha256.Size*2 {
			return errors.New("resource SHA256 must contain 64 hexadecimal characters")
		}
		if _, err := hex.DecodeString(normalized); err != nil {
			return errors.New("resource SHA256 is invalid")
		}
	}
	return nil
}

func requestURLs(request Request) []string {
	values := make([]string, 0, 1+len(request.EquivalentMirrorURLs))
	values = append(values, request.URL)
	values = append(values, request.EquivalentMirrorURLs...)
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func resumeStateMatchesRequest(state resumeState, request Request, allowedURLs []string) bool {
	if !containsString(allowedURLs, state.SelectedURL) {
		return false
	}
	expected, expectedKnown := expectedSize(request)
	if expectedKnown && (!state.TotalKnown || state.Total != expected) {
		return false
	}
	if etag := normalizeStrongETag(request.Identity.ETag); etag != "" && state.ETag != etag {
		return false
	}
	if request.Identity.ETag == "" && request.Identity.LastModified != "" &&
		!sameHTTPDate(state.LastModified, request.Identity.LastModified) {
		return false
	}
	if digest := requestSHA256(request); digest != "" && state.ExpectedSHA256 != digest {
		return false
	}
	return true
}

func validateExpectedValidator(identity ResourceIdentity, actual ResourceValidator) error {
	if expected := normalizeStrongETag(identity.ETag); expected != "" && actual.ETag != expected {
		return fmt.Errorf("etag=%q expected=%q", actual.ETag, expected)
	}
	if strings.TrimSpace(identity.ETag) == "" && strings.TrimSpace(identity.LastModified) != "" &&
		!sameHTTPDate(actual.LastModified, identity.LastModified) {
		return fmt.Errorf("last-modified=%q expected=%q", actual.LastModified, identity.LastModified)
	}
	return nil
}

func validatorMatchesState(state resumeState, actual ResourceValidator) bool {
	if state.ETag != "" {
		return actual.ETag != "" && actual.ETag == state.ETag
	}
	if state.LastModified != "" {
		return actual.LastModified != "" && sameHTTPDate(actual.LastModified, state.LastModified)
	}
	return false
}

func responseValidator(resp *http.Response) ResourceValidator {
	return ResourceValidator{
		ETag:         normalizeStrongETag(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
	}
}

func normalizeStrongETag(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(strings.ToLower(value), "w/") {
		return ""
	}
	return value
}

func ifRangeValue(state resumeState) string {
	if state.ETag != "" {
		return state.ETag
	}
	return state.LastModified
}

func sameHTTPDate(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return a != ""
	}
	at, aErr := http.ParseTime(a)
	bt, bErr := http.ParseTime(b)
	return aErr == nil && bErr == nil && at.Equal(bt)
}

func responseTotal(request Request, contentLength int64) (int64, bool, error) {
	expected, expectedKnown := expectedSize(request)
	if contentLength >= 0 {
		if expectedKnown && expected != contentLength {
			return 0, false, fmt.Errorf("Content-Length=%d expected=%d", contentLength, expected)
		}
		return contentLength, true, nil
	}
	if expectedKnown {
		return expected, true, nil
	}
	return 0, false, nil
}

func expectedSize(request Request) (int64, bool) {
	if request.Identity.ExpectedSize > 0 {
		return request.Identity.ExpectedSize, true
	}
	return 0, false
}

func expectedSizeValue(request Request) int64 {
	value, _ := expectedSize(request)
	return value
}

func requestSHA256(request Request) string {
	return normalizeSHA256(request.Identity.SHA256)
}

func normalizeSHA256(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type parsedContentRange struct {
	start       int64
	end         int64
	total       int64
	totalKnown  bool
	unsatisfied bool
}

func parseContentRange(value string) (parsedContentRange, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return parsedContentRange{}, fmt.Errorf("invalid Content-Range %q", value)
	}
	if strings.HasPrefix(fields[1], "*/") {
		total, err := strconv.ParseInt(strings.TrimPrefix(fields[1], "*/"), 10, 64)
		if err != nil || total < 0 {
			return parsedContentRange{}, fmt.Errorf("invalid Content-Range %q", value)
		}
		return parsedContentRange{total: total, totalKnown: true, unsatisfied: true}, nil
	}
	parts := strings.Split(fields[1], "/")
	if len(parts) != 2 || parts[1] == "*" {
		return parsedContentRange{}, fmt.Errorf("invalid Content-Range %q", value)
	}
	bounds := strings.Split(parts[0], "-")
	if len(bounds) != 2 {
		return parsedContentRange{}, fmt.Errorf("invalid Content-Range %q", value)
	}
	start, startErr := strconv.ParseInt(bounds[0], 10, 64)
	end, endErr := strconv.ParseInt(bounds[1], 10, 64)
	total, totalErr := strconv.ParseInt(parts[1], 10, 64)
	if startErr != nil || endErr != nil || totalErr != nil || start < 0 || end < start || total <= end {
		return parsedContentRange{}, fmt.Errorf("invalid Content-Range %q", value)
	}
	return parsedContentRange{start: start, end: end, total: total, totalKnown: true}, nil
}

func reportReset(progress ProgressReporter, total int64) {
	if progress != nil {
		progress(Progress{Downloaded: 0, Total: total, Kind: ProgressReset})
	}
}

func shouldSafeRestart(err error, policy ResumePolicy) bool {
	if policy.RequireRange || !policy.RestartWhenRangeIgnored {
		return false
	}
	var fetchErr *Error
	if !errors.As(err, &fetchErr) {
		return false
	}
	return fetchErr.Kind == ErrorIdentityMismatch || fetchErr.Kind == ErrorRangeUnsupported
}

func classifyDoError(ctx context.Context, rawURL string, err error) error {
	if errors.Is(context.Cause(ctx), errNoProgressTimeout) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Kind: ErrorTimeout, URL: rawURL, Err: timeoutCause(ctx, err)}
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return &Error{Kind: ErrorTimeout, URL: rawURL, Err: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return &Error{Kind: ErrorCanceled, URL: rawURL, Err: err}
	}
	return &Error{Kind: ErrorNetwork, URL: rawURL, Err: err}
}

func addErrorURL(err error, rawURL string) error {
	var fetchErr *Error
	if errors.As(err, &fetchErr) {
		if fetchErr.URL == "" {
			fetchErr.URL = rawURL
		}
		return fetchErr
	}
	return err
}

func withAttempts(err error, attempts int) error {
	var fetchErr *Error
	if errors.As(err, &fetchErr) {
		fetchErr.Attempts = attempts
	}
	return err
}

func isIntegrityError(err error) bool {
	var fetchErr *Error
	return errors.As(err, &fetchErr) && (fetchErr.Kind == ErrorIntegrity || fetchErr.Kind == ErrorIdentityMismatch)
}

func applyHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func applyTransferHeaders(req *http.Request, headers map[string]string) {
	applyHeaders(req, headers)
	// Range offsets and validators describe the transferred representation.
	// Letting net/http transparently decode gzip would make a partial file's
	// byte offsets incompatible with a later Range request.
	req.Header.Set("Accept-Encoding", "identity")
}

func isRetryable(err error, policy RetryPolicy) bool {
	var fetchErr *Error
	if !errors.As(err, &fetchErr) {
		return false
	}
	if fetchErr.Kind == ErrorNetwork || fetchErr.Kind == ErrorTimeout {
		return true
	}
	if fetchErr.Kind != ErrorStatusCode {
		return false
	}
	if len(policy.RetryableStatusCodes) == 0 {
		return fetchErr.StatusCode >= 500
	}
	return statusExplicitlyRetryable(policy, fetchErr.StatusCode)
}

func statusExplicitlyRetryable(policy RetryPolicy, statusCode int) bool {
	for _, code := range policy.RetryableStatusCodes {
		if code == statusCode {
			return true
		}
	}
	return false
}

var errNoProgressTimeout = errors.New("fetch made no progress before timeout")

type noProgressWatchdog struct {
	timeout time.Duration
	mu      sync.Mutex
	last    time.Time
	cancel  context.CancelCauseFunc
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func withNoProgressWatchdog(parent context.Context, timeout time.Duration) (context.Context, *noProgressWatchdog) {
	ctx, cancel := context.WithCancelCause(parent)
	watchdog := &noProgressWatchdog{
		timeout: timeout,
		cancel:  cancel,
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	watchdog.last = time.Now()
	go watchdog.run(ctx, cancel)
	return ctx, watchdog
}

func effectiveNoProgressTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return DefaultNoProgressTimeout
}

func (watchdog *noProgressWatchdog) Touch() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	watchdog.last = time.Now()
	watchdog.mu.Unlock()
	select {
	case watchdog.wake <- struct{}{}:
	default:
	}
}

func (watchdog *noProgressWatchdog) Stop() {
	if watchdog == nil {
		return
	}
	watchdog.once.Do(func() { close(watchdog.stop) })
	<-watchdog.done
	// Detach the derived context from its parent after a successful attempt.
	// A prior timeout cause, if any, is preserved by context.CancelCauseFunc.
	watchdog.cancel(context.Canceled)
}

func (watchdog *noProgressWatchdog) run(ctx context.Context, cancel context.CancelCauseFunc) {
	defer close(watchdog.done)
	timer := time.NewTimer(watchdog.timeout)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			remaining := watchdog.remaining()
			if remaining <= 0 {
				cancel(errNoProgressTimeout)
				return
			}
			timer.Reset(remaining)
		case <-watchdog.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			remaining := watchdog.remaining()
			if remaining <= 0 {
				remaining = time.Nanosecond
			}
			timer.Reset(remaining)
		case <-watchdog.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (watchdog *noProgressWatchdog) remaining() time.Duration {
	watchdog.mu.Lock()
	lastProgress := watchdog.last
	watchdog.mu.Unlock()
	return watchdog.timeout - time.Since(lastProgress)
}

type activityReadCloser struct {
	io.ReadCloser
	touch func()
}

func (reader *activityReadCloser) Read(buffer []byte) (int, error) {
	count, err := reader.ReadCloser.Read(buffer)
	if count > 0 && reader.touch != nil {
		reader.touch()
	}
	return count, err
}

func timeoutCause(ctx context.Context, fallback error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return fallback
}

func transferContextError(ctx context.Context) error {
	cause := context.Cause(ctx)
	if errors.Is(cause, errNoProgressTimeout) || errors.Is(cause, context.DeadlineExceeded) {
		return &Error{Kind: ErrorTimeout, Err: cause}
	}
	if err := ctx.Err(); err != nil {
		return &Error{Kind: ErrorCanceled, Err: err}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
