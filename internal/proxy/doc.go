// Package proxy implements the localhost-only MITM proxy used for WeChat
// Channels detection.
//
// CONNECT handling is intentionally narrow: only hosts in MITMDomains are
// decrypted so scripts can be injected into WeChat page/static-resource domains.
// Video CDN domains and all unrelated HTTPS traffic pass through as ordinary
// tunnels. The proxy listener binds to 127.0.0.1 and is intended to be reached
// only through the local system proxy configuration.
//
// Runtime upstream proxy changes are applied to new requests without restarting
// the proxy. Response rewriting reads raw bodies first and restores pass-through
// bodies when an unsupported Content-Encoding prevents modification.
package proxy
