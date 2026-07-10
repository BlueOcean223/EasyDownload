// Package wechat provides video downloading functionality for WeChat Channels (视频号).
//
// Detection keeps signed media URLs, headers, decode keys, and platform content
// identifiers private on the backend. Wails receives only an opaque candidate ID;
// adapter-owned PlatformData contains the complete restartable execution input.
//
// RunTask uses the TaskExecutionContext Fetcher to produce a validated encrypted
// temporary file. Encrypted content is copied to a separate derived temporary before
// decryption, so a failed decrypt preserves the trusted fetch for retry. Only
// PublishFinal may expose the verified result at the manager-reserved final path.
// Pause and shutdown preserve partials; cancel and removal clean task-owned encrypted
// and derived temporaries only after the worker has exited.
// The adapter rejects an unknown PlatformDataVersion before decoding PlatformData.
//
// See docs/wechat-channels-download-principle.md for the complete flow.
package wechat
