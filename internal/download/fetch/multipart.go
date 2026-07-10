package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

const multipartWorkers = 4

type multipartIdentity struct {
	total     int64
	validator ResourceValidator
}

type multipartPart struct {
	index int
	start int64
	end   int64
	path  string
}

// downloadMultipartOnce performs a range probe, downloads disjoint ranges,
// validates every range against one resource identity, and only then assembles
// the caller-owned temporary artifact. A server that does not honor ranges is
// handled by the ordinary single-stream path; enabling multipart is therefore
// an optimization request, never a relaxation of integrity checks.
func (c *Client) downloadMultipartOnce(
	ctx context.Context,
	rawURL string,
	request Request,
	destination resolvedDestination,
	progress ProgressReporter,
) (Result, error) {
	identity, supported, err := c.probeMultipartIdentity(ctx, rawURL, request)
	if err != nil {
		return Result{}, err
	}
	policy := request.MultipartPolicy
	if !supported || identity.total <= policy.PartSize || policy.Threshold > 0 && identity.total < policy.Threshold {
		return c.downloadOnce(ctx, rawURL, []string{rawURL}, request, destination, progress)
	}

	if err := c.resetResumeFiles(destination); err != nil {
		return Result{}, &Error{Kind: ErrorResumeState, URL: rawURL, Err: err}
	}
	reportReset(progress, identity.total)
	state := resumeState{
		Version:        resumeStateVersion,
		SelectedURL:    rawURL,
		ETag:           identity.validator.ETag,
		LastModified:   identity.validator.LastModified,
		Total:          identity.total,
		TotalKnown:     true,
		ExpectedSHA256: requestSHA256(request),
	}
	if err := c.sidecars.Save(destination.statePath, state); err != nil {
		return Result{}, &Error{Kind: ErrorResumeState, URL: rawURL, Err: err}
	}

	partsDir := destination.temporaryPath + ".multipart"
	if err := os.RemoveAll(partsDir); err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	if err := os.MkdirAll(partsDir, 0o700); err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	removeParts := true
	defer func() {
		if removeParts {
			_ = os.RemoveAll(partsDir)
		}
	}()

	parts := buildMultipartParts(identity.total, policy.PartSize, partsDir)
	if err := c.fetchMultipartParts(ctx, rawURL, request, identity, parts, progress); err != nil {
		return Result{}, err
	}
	if err := assembleMultipart(destination.temporaryPath, parts, identity.total); err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	if err := os.RemoveAll(partsDir); err != nil {
		return Result{}, &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	removeParts = false
	return c.verifyCompleted(rawURL, request, destination, identity.total, identity.total, false, identity.validator)
}

func (c *Client) probeMultipartIdentity(ctx context.Context, rawURL string, request Request) (multipartIdentity, bool, error) {
	requestContext, watchdog := withNoProgressWatchdog(ctx, effectiveNoProgressTimeout(request.NoProgressTimeout))
	defer watchdog.Stop()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, rawURL, nil)
	if err != nil {
		return multipartIdentity{}, false, err
	}
	applyTransferHeaders(req, request.Headers)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := c.doer.Do(req)
	if err != nil {
		return multipartIdentity{}, false, classifyDoError(requestContext, rawURL, err)
	}
	watchdog.Touch()
	resp.Body = &activityReadCloser{ReadCloser: resp.Body, touch: watchdog.Touch}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return multipartIdentity{}, false, nil
	}
	if resp.StatusCode != http.StatusPartialContent {
		return multipartIdentity{}, false, &Error{Kind: ErrorStatusCode, URL: rawURL, StatusCode: resp.StatusCode}
	}
	contentRange, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil || contentRange.unsatisfied || !contentRange.totalKnown || contentRange.start != 0 || contentRange.end != 0 {
		if err == nil {
			err = errors.New("multipart probe returned an invalid byte range")
		}
		return multipartIdentity{}, false, &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: err}
	}
	probeBody, err := io.ReadAll(io.LimitReader(resp.Body, 2))
	if err != nil {
		if contextErr := transferContextError(requestContext); contextErr != nil {
			return multipartIdentity{}, false, addErrorURL(contextErr, rawURL)
		}
		return multipartIdentity{}, false, &Error{Kind: ErrorNetwork, URL: rawURL, Err: err}
	}
	if len(probeBody) != 1 {
		return multipartIdentity{}, false, &Error{Kind: ErrorIntegrity, URL: rawURL, Err: fmt.Errorf("multipart probe body length=%d expected=1", len(probeBody))}
	}
	validator := responseValidator(resp)
	if err := validateExpectedValidator(request.Identity, validator); err != nil {
		return multipartIdentity{}, false, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, Err: err}
	}
	if validator.ETag == "" && validator.LastModified == "" {
		// Without a response validator separate requests cannot be proven to
		// belong to one entity. The sequential path remains safe.
		return multipartIdentity{}, false, nil
	}
	expected, expectedKnown := expectedSize(request)
	if expectedKnown && expected != contentRange.total {
		return multipartIdentity{}, false, &Error{Kind: ErrorIdentityMismatch, URL: rawURL, Err: fmt.Errorf("multipart total=%d expected=%d", contentRange.total, expected)}
	}
	if request.MaxBytes > 0 && contentRange.total > request.MaxBytes {
		return multipartIdentity{}, false, &Error{Kind: ErrorSizeLimit, URL: rawURL, Err: fmt.Errorf("total=%d limit=%d", contentRange.total, request.MaxBytes)}
	}
	return multipartIdentity{total: contentRange.total, validator: validator}, true, nil
}

func buildMultipartParts(total, partSize int64, dir string) []multipartPart {
	count := int((total + partSize - 1) / partSize)
	parts := make([]multipartPart, 0, count)
	for start, index := int64(0), 0; start < total; start, index = start+partSize, index+1 {
		end := start + partSize - 1
		if end >= total {
			end = total - 1
		}
		parts = append(parts, multipartPart{
			index: index,
			start: start,
			end:   end,
			path:  filepath.Join(dir, fmt.Sprintf("part-%06d", index)),
		})
	}
	return parts
}

func (c *Client) fetchMultipartParts(
	ctx context.Context,
	rawURL string,
	request Request,
	identity multipartIdentity,
	parts []multipartPart,
	progress ProgressReporter,
) error {
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan multipartPart)
	errCh := make(chan error, 1)
	workerCount := min(multipartWorkers, len(parts))
	var workers sync.WaitGroup
	var progressMu sync.Mutex
	var downloaded int64

	reportPartProgress := func(partDownloaded *int64, current int64) {
		progressMu.Lock()
		delta := current - *partDownloaded
		if delta > 0 {
			*partDownloaded = current
			downloaded += delta
			if progress != nil {
				progress(Progress{Downloaded: downloaded, Total: identity.total, Kind: ProgressUpdate})
			}
		}
		progressMu.Unlock()
	}

	worker := func() {
		defer workers.Done()
		for part := range jobs {
			if workerContext.Err() != nil {
				return
			}
			var partDownloaded int64
			err := c.fetchMultipartPart(workerContext, rawURL, request, identity, part, func(current int64) {
				reportPartProgress(&partDownloaded, current)
			})
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
				return
			}
		}
	}

	workers.Add(workerCount)
	for range workerCount {
		go worker()
	}
	for _, part := range parts {
		select {
		case jobs <- part:
		case <-workerContext.Done():
			break
		}
		if workerContext.Err() != nil {
			break
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	if err := ctx.Err(); err != nil {
		return classifyDoError(ctx, rawURL, err)
	}
	if downloaded != identity.total {
		return &Error{Kind: ErrorIntegrity, URL: rawURL, Err: fmt.Errorf("multipart downloaded=%d total=%d", downloaded, identity.total)}
	}
	return nil
}

func (c *Client) fetchMultipartPart(
	ctx context.Context,
	rawURL string,
	request Request,
	identity multipartIdentity,
	part multipartPart,
	progress func(int64),
) error {
	requestContext, watchdog := withNoProgressWatchdog(ctx, effectiveNoProgressTimeout(request.NoProgressTimeout))
	defer watchdog.Stop()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	applyTransferHeaders(req, request.Headers)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", part.start, part.end))
	if identity.validator.ETag != "" {
		req.Header.Set("If-Range", identity.validator.ETag)
	} else {
		req.Header.Set("If-Range", identity.validator.LastModified)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return classifyDoError(requestContext, rawURL, err)
	}
	watchdog.Touch()
	resp.Body = &activityReadCloser{ReadCloser: resp.Body, touch: watchdog.Touch}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		kind := ErrorStatusCode
		if resp.StatusCode == http.StatusOK {
			kind = ErrorRangeUnsupported
		}
		return &Error{Kind: kind, URL: rawURL, StatusCode: resp.StatusCode}
	}
	contentRange, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil || contentRange.unsatisfied || !contentRange.totalKnown ||
		contentRange.start != part.start || contentRange.end != part.end || contentRange.total != identity.total {
		if err == nil {
			err = fmt.Errorf("multipart part %d returned range %d-%d/%d", part.index, contentRange.start, contentRange.end, contentRange.total)
		}
		return &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: err}
	}
	if !validatorMatchesState(resumeState{ETag: identity.validator.ETag, LastModified: identity.validator.LastModified}, responseValidator(resp)) {
		return &Error{Kind: ErrorIdentityMismatch, URL: rawURL, StatusCode: resp.StatusCode, Err: fmt.Errorf("multipart part %d validator changed", part.index)}
	}
	expected := part.end - part.start + 1
	if resp.ContentLength >= 0 && resp.ContentLength != expected {
		return &Error{Kind: ErrorIntegrity, URL: rawURL, StatusCode: resp.StatusCode, Err: fmt.Errorf("multipart part %d Content-Length=%d expected=%d", part.index, resp.ContentLength, expected)}
	}
	file, err := os.OpenFile(part.path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return &Error{Kind: ErrorDestination, URL: rawURL, Err: err}
	}
	copied, copyErr := copyWithProgress(requestContext, file, resp.Body, 0, expected, true, identity.total, request.MaxBytes, func(update Progress) {
		progress(update.Downloaded)
	})
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return errors.Join(addErrorURL(copyErr, rawURL), syncErr, closeErr)
	}
	if syncErr != nil {
		return &Error{Kind: ErrorDestination, URL: rawURL, Err: errors.Join(syncErr, closeErr)}
	}
	if closeErr != nil {
		return &Error{Kind: ErrorDestination, URL: rawURL, Err: closeErr}
	}
	if copied != expected {
		return &Error{Kind: ErrorIntegrity, URL: rawURL, Err: fmt.Errorf("multipart part %d copied=%d expected=%d", part.index, copied, expected)}
	}
	return nil
}

func assembleMultipart(temporaryPath string, parts []multipartPart, total int64) error {
	output, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	closeOutput := true
	defer func() {
		if closeOutput {
			_ = output.Close()
		}
	}()
	written := int64(0)
	for _, part := range parts {
		input, err := os.Open(part.path)
		if err != nil {
			return err
		}
		copied, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if copied != part.end-part.start+1 {
			return fmt.Errorf("multipart part %d assembly length=%d", part.index, copied)
		}
		written += copied
	}
	if written != total {
		return fmt.Errorf("multipart assembly length=%d expected=%d", written, total)
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	closeOutput = false
	return nil
}
