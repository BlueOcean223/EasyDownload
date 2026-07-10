package downloader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/infra/logger"
)

type publishWarning struct {
	Code    string
	Message string
}

type publishOutcome struct {
	Committed bool
	Warnings  []publishWarning
}

// publishNoReplace makes finalPath visible atomically without replacing an
// existing entry. A non-nil error means the visibility commit did not happen.
// Once commitNoReplace succeeds, directory sync and fallback-source cleanup are
// best effort and are returned as diagnostics instead of reversing success.
func publishNoReplace(temporaryPath, finalPath string) (publishOutcome, error) {
	if strings.TrimSpace(temporaryPath) == "" || strings.TrimSpace(finalPath) == "" {
		return publishOutcome{}, errors.New("temporary and final paths are required")
	}
	temporaryPath = filepath.Clean(temporaryPath)
	finalPath = filepath.Clean(finalPath)

	sourceConsumed, err := commitNoReplace(temporaryPath, finalPath)
	if err != nil {
		return publishOutcome{}, err
	}
	outcome := finishPublishedFile(temporaryPath, finalPath, sourceConsumed, syncParentDirectory, os.Remove)
	for _, warning := range outcome.Warnings {
		logger.Warn("Published final file with post-commit warning: code=%s final=%s err=%s", warning.Code, finalPath, warning.Message)
	}
	return outcome, nil
}

func finishPublishedFile(
	temporaryPath string,
	finalPath string,
	sourceConsumed bool,
	syncDirectory func(string) error,
	remove func(string) error,
) publishOutcome {
	outcome := publishOutcome{Committed: true}
	appendWarning := func(code string, err error) {
		if err == nil {
			return
		}
		outcome.Warnings = append(outcome.Warnings, publishWarning{Code: code, Message: err.Error()})
	}

	appendWarning("publish.final_directory_sync_failed", syncDirectory(finalPath))
	if sourceConsumed {
		if !sameParentDirectory(temporaryPath, finalPath) {
			appendWarning("publish.temporary_directory_sync_failed", syncDirectory(temporaryPath))
		}
		return outcome
	}

	if err := remove(temporaryPath); err != nil {
		if !os.IsNotExist(err) {
			appendWarning("publish.temporary_cleanup_failed", err)
		}
		return outcome
	}
	appendWarning("publish.temporary_directory_sync_failed", syncDirectory(temporaryPath))
	return outcome
}

func sameParentDirectory(left, right string) bool {
	leftParent, _, leftErr := canonicalOutputPath(filepath.Dir(left))
	rightParent, _, rightErr := canonicalOutputPath(filepath.Dir(right))
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(filepath.Dir(left)) == filepath.Clean(filepath.Dir(right))
	}
	return leftParent == rightParent
}

func linkNoReplace(source, destination string) (bool, error) {
	if err := os.Link(source, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("%w: %s", ErrOutputExists, destination)
		}
		return false, fmt.Errorf("filesystem does not support atomic no-replace publish: %w", err)
	}
	return false, nil
}

func applyPublishWarnings(draft downloadtask.TaskArtifactDraft, outcome publishOutcome) downloadtask.TaskArtifactDraft {
	if len(outcome.Warnings) == 0 {
		return draft
	}
	metadata := make(map[string]string, len(draft.Metadata)+len(outcome.Warnings)*2+1)
	for key, value := range draft.Metadata {
		metadata[key] = value
	}
	metadata["publishWarningCount"] = fmt.Sprintf("%d", len(outcome.Warnings))
	for index, warning := range outcome.Warnings {
		metadata[fmt.Sprintf("publishWarning.%d.code", index)] = warning.Code
		metadata[fmt.Sprintf("publishWarning.%d.message", index)] = warning.Message
	}
	draft.Metadata = metadata
	return draft
}
