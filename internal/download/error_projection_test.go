package downloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"
)

func TestTaskErrorFromErrorPreservesStructuredPlatformError(t *testing.T) {
	private := &downloadtask.TaskError{
		Code:       "bilibili.auth_required",
		Category:   downloadtask.TaskErrorCategoryPlatform,
		Message:    "登录状态已失效",
		Retryable:  true,
		UserAction: "请重新登录哔哩哔哩",
		Cause:      "GET https://example.com/video?token=secret Authorization: Bearer key-secret",
		Metadata: map[string]string{
			"url":           "https://media.example.com/video?token=secret",
			"Authorization": "Bearer key-secret",
			"statusCode":    "403",
		},
	}
	got := taskErrorFromError(fmt.Errorf("adapter failed: %w", private))
	if got == private || got.Code != private.Code || got.UserAction != private.UserAction || got.Cause != private.Cause {
		t.Fatalf("structured error was not deep-cloned and preserved: %#v", got)
	}
	got.Metadata["statusCode"] = "changed"
	if private.Metadata["statusCode"] != "403" {
		t.Fatal("structured error metadata was shallow-copied")
	}
}

func TestPublicTaskErrorProjectionNeverLeaksSignedURLCauseOrCredentials(t *testing.T) {
	signedURL := "https://media.example.com/video?token=signed-secret"
	private := taskErrorFromError(&fetch.Error{
		Kind:       fetch.ErrorStatusCode,
		URL:        signedURL,
		StatusCode: 403,
		Attempts:   2,
		Err:        errors.New("Authorization: Bearer auth-secret decodeKey=decode-secret"),
	})
	task := &DownloadTask{
		ID:              "task-1",
		Status:          StatusFailed,
		Error:           private.Cause,
		LastError:       private.Cause,
		LastErrorDetail: private,
	}
	serialized, err := json.Marshal(task.PublicSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{signedURL, "signed-secret", "auth-secret", "decode-secret", "Authorization", "decodeKey", "cause"} {
		if strings.Contains(string(serialized), secret) {
			t.Errorf("public error projection leaked %q: %s", secret, serialized)
		}
	}
	if !strings.Contains(string(serialized), `"urlHost":"media.example.com"`) || !strings.Contains(string(serialized), `"statusCode":"403"`) {
		t.Fatalf("public error projection lost allowlisted diagnostics: %s", serialized)
	}
}

func TestUnexpectedErrorUsesStablePublicMessage(t *testing.T) {
	private := taskErrorFromError(errors.New("GET https://private.example/?token=secret header=Bearer key-secret"))
	if private.Cause == "" || private.Message == private.Cause {
		t.Fatalf("unexpected error did not separate public message and private cause: %#v", private)
	}
	public := publicTaskError(private)
	serialized, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "private.example") || strings.Contains(string(serialized), "key-secret") {
		t.Fatalf("unexpected public error leaked private cause: %s", serialized)
	}
}
