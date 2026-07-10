package downloader_test

import (
	"errors"
	"strings"
	"testing"

	downloader "EasyDownload/internal/download"
	"EasyDownload/internal/download/bilibili"
	"EasyDownload/internal/download/douyin"
	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/download/wechat"
	"EasyDownload/internal/download/xiaohongshu"
)

func TestAllProductionAdaptersRejectUnknownPlatformDataVersion(t *testing.T) {
	adapters := []downloadtask.PlatformAdapter{
		downloader.NewGenericAdapter(),
		wechat.NewAdapter(),
		bilibili.NewAdapter(),
		douyin.NewAdapter(),
		xiaohongshu.NewAdapter(),
	}
	for _, adapter := range adapters {
		t.Run(string(adapter.ID()), func(t *testing.T) {
			err := adapter.ValidateTask(downloadtask.TaskSnapshot{PlatformDataVersion: 99})
			if err == nil {
				t.Fatalf("ValidateTask error=%v, want fail-closed version rejection", err)
			}
			message := err.Error()
			var taskErr *downloadtask.TaskError
			cause := ""
			if errors.As(err, &taskErr) {
				cause = taskErr.Cause
				message += " " + cause
			}
			if !strings.Contains(message, "unsupported platform data version") {
				t.Fatalf("ValidateTask error=%v cause=%q, want fail-closed version rejection", err, cause)
			}
		})
	}
}
