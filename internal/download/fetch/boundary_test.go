package fetch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlatformPackagesDoNotConstructPrivateFetchersOrByteCopyHTTPResponses(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	downloadDir := filepath.Dir(filepath.Dir(thisFile))
	paths := []string{
		filepath.Join(downloadDir, "generic_adapter.go"),
		filepath.Join(downloadDir, "wechat"),
		filepath.Join(downloadDir, "xiaohongshu"),
		filepath.Join(downloadDir, "douyin"),
		filepath.Join(downloadDir, "bilibili"),
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		require.NoError(t, err)
		files := []string{path}
		if info.IsDir() {
			entries, readErr := os.ReadDir(path)
			require.NoError(t, readErr)
			files = files[:0]
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
					continue
				}
				files = append(files, filepath.Join(path, entry.Name()))
			}
		}
		for _, file := range files {
			data, readErr := os.ReadFile(file)
			require.NoError(t, readErr)
			source := string(data)
			require.NotContains(t, source, "fetch.New(", "%s must use TaskExecutionContext.Fetcher", file)
			// Platform API clients may issue JSON/probe requests and post-process
			// local files, but they may not combine a private HTTP request with a
			// response-to-file copy loop. Media bytes belong to Fetch.
			privateRequest := strings.Contains(source, "http.NewRequest") || strings.Contains(source, "http.NewRequestWithContext")
			responseCopy := strings.Contains(source, "io.Copy(") && (strings.Contains(source, "os.Create(") || strings.Contains(source, "os.OpenFile("))
			require.False(t, privateRequest && responseCopy, "%s contains a private HTTP response-to-file loop", file)
		}
	}
}

func TestFetchHasNoPlatformHostOrHeaderPolicy(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr)
		source := strings.ToLower(string(data))
		for _, forbidden := range []string{
			"bilibili.com",
			"douyin.com",
			"xiaohongshu.com",
			"channels.weixin.qq.com",
			`"referer"`,
			`"user-agent"`,
		} {
			require.NotContains(t, source, forbidden, "%s contains platform-specific policy", entry.Name())
		}
	}
}

func TestProductionCompositionRootInjectsSharedFetcher(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	data, err := os.ReadFile(filepath.Join(repositoryRoot, "app.go"))
	require.NoError(t, err)
	source := string(data)
	require.Contains(t, source, `"EasyDownload/internal/download/fetch"`)
	require.Contains(t, source, "SetExecutionCapabilities(fetch.New(nil)",
		"production must inject one shared Fetcher at the composition root")
}
