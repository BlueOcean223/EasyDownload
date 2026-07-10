package downloader

import (
	"context"
	"encoding/json"

	downloadtask "EasyDownload/internal/download/task"
)

// inertTestAdapter makes registration explicit in manager tests that exercise
// task bookkeeping rather than platform execution.
type inertTestAdapter struct {
	id downloadtask.PlatformID
}

func (adapter inertTestAdapter) ID() downloadtask.PlatformID { return adapter.id }

func (inertTestAdapter) ValidateTask(downloadtask.TaskSnapshot) error { return nil }

func (inertTestAdapter) RunTask(context.Context, downloadtask.TaskSnapshot, downloadtask.TaskExecutionContext) error {
	return nil
}

func (inertTestAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}

func registerInertTestAdapter(dm *DownloadManager, platformID downloadtask.PlatformID) error {
	return dm.RegisterPlatformAdapter(inertTestAdapter{id: platformID})
}

func createStrictTestTask(
	dm *DownloadManager,
	id string,
	rawURL string,
	title string,
	platformID downloadtask.PlatformID,
	platformData ...json.RawMessage,
) (*DownloadTask, error) {
	data := json.RawMessage(nil)
	if len(platformData) > 0 {
		data = append(json.RawMessage(nil), platformData[0]...)
	} else {
		encoded, err := json.Marshal(downloadtask.GenericPlatformData{URL: rawURL})
		if err != nil {
			return nil, err
		}
		data = encoded
	}
	return dm.CreateTask(TaskCreationInput{
		ID:                  id,
		PlatformID:          platformID,
		Title:               title,
		DisplaySource:       string(platformID),
		SuggestedFilename:   title,
		SuggestedExtension:  ".mp4",
		PlatformDataVersion: 1,
		PlatformData:        data,
	})
}
