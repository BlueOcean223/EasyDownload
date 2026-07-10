package downloader

import (
	"context"
	"testing"

	downloadtask "EasyDownload/internal/download/task"

	"github.com/stretchr/testify/require"
)

func TestPlatformRegistryRegisterAndLookup(t *testing.T) {
	registry := NewPlatformRegistry()
	_, ok := registry.Get(downloadtask.PlatformWeChat)
	require.False(t, ok)

	adapter := fakePlatformAdapter{id: downloadtask.PlatformWeChat}
	require.NoError(t, registry.Register(adapter))
	require.Error(t, registry.Register(adapter))
	got, ok := registry.Get(downloadtask.PlatformWeChat)
	require.True(t, ok)
	require.Equal(t, downloadtask.PlatformWeChat, got.ID())
}

func TestRegisterPlatformAdapterRejectsInvalidAdapter(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	require.Error(t, dm.RegisterPlatformAdapter(fakePlatformAdapter{}))
}

type fakePlatformAdapter struct {
	id downloadtask.PlatformID
}

func (f fakePlatformAdapter) ID() downloadtask.PlatformID { return f.id }
func (fakePlatformAdapter) ValidateTask(downloadtask.TaskSnapshot) error {
	return nil
}
func (fakePlatformAdapter) RunTask(context.Context, downloadtask.TaskSnapshot, downloadtask.TaskExecutionContext) error {
	return nil
}
func (fakePlatformAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}
