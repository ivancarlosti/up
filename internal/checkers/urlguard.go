package checkers

import (
	"fmt"
	"regexp"
)

// probeURLPattern matches the URL a probe is allowed to dial: an http(s) or
// ws(s) scheme, a host name or an IP literal (an IPv6 literal between
// brackets), an optional port, an optional path and an optional query string.
//
// The URL of a monitor is typed by the operator - and written by an API client -
// so it is the untrusted value of the whole probe pipeline: nothing reaches a
// socket before it matches the shape of a dialable URL. What the regexp does not
// cover is refused:
//
//   - another scheme (`file:`, `gopher:`, `ftp:`, `data:`), which is how a
//     request-forgery probe would be pointed at a local file or at a protocol
//     that is not HTTP at all;
//   - a relative or protocol-relative target (`/admin`, `//169.254.169.254`),
//     which has no host of its own;
//   - embedded credentials (`https://user:pass@host`): the auth options of the
//     monitor are the supported way, and the URL is the one string that ends up
//     in logs and error messages;
//   - a whitespace or control character anywhere in the URL, which the
//     WebSocket upgrade would turn into a header injection;
//   - a host that is empty (`http:///path`).
//
// This is the same treatment the cluster join target gets
// (services.clusterPrimaryURL), and the reason both exist is the same query:
// `go/request-forgery` / SSRF.
var probeURLPattern = regexp.MustCompile(`(?i)^(?:https?|wss?)://(?:\[[0-9a-f:.]+\]|[^\s/?#:@\\]+)(?::[0-9]{1,5})?(?:/[^\s?#]*)?(?:\?[^\s#]*)?(?:#\S*)?$`)

// invalidProbeURLMessage explains a URL the guard refused, naming the schemes of
// the probe that refused it so the operator sees what was expected.
func invalidProbeURLMessage(rawURL, schemes string) string {
	return fmt.Sprintf("invalid url %q: expected %s followed by a host and an optional port, path or query", rawURL, schemes)
}
