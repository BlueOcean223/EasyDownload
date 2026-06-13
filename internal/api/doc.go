// Package api exposes the localhost-only HTTP API used by the desktop frontend
// and injected browser scripts.
//
// Browser-facing routes use a per-process random token. Callers can provide the
// token via the X-EasyDownload-Token header, or as a token query parameter for
// image/video tags that cannot set custom headers. CORS is restricted to local
// desktop/development origins and known injected origins.
//
// The image proxy performs SSRF checks before and during dialing: unsafe
// loopback, private, link-local, multicast, unspecified and CGNAT addresses are
// blocked, redirects are revalidated, and responses must be images within the
// configured size limit.
package api
