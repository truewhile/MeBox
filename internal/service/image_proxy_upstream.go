package service

import (
	"net/url"
	"strconv"
	"strings"
)

// imageCacheKeyURL normalizes an upstream image URL before it is hashed into a
// disk cache key. Credential query parameters are dropped: a remote Emby token
// is per-account, so removing it cannot make two different images collide, but
// it stops a token rotation from invalidating every cached poster at once.
func imageCacheKeyURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	q := u.Query()
	dropped := false
	for key := range q {
		switch strings.ToLower(key) {
		case "api_key", "apikey", "x-emby-token", "x-mediabrowser-token":
			q.Del(key)
			dropped = true
		}
	}
	if !dropped {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// upstreamImageFetchURL forwards the client's thumbnail request to a configured
// remote Emby mount. The remote server can produce the thumbnail itself from
// maxWidth/maxHeight/quality, so the proxy transfers a few dozen KB instead of
// the full-size original and skips the local decode+scale entirely (measured at
// ~400ms for a single 2892×4096 poster).
//
// Other upstreams (TMDb, Douban, adult sites, ...) do not honour these
// parameters, so their originals are still fetched and scaled locally.
func (p *ImageProxy) upstreamImageFetchURL(raw string, opts imageResizeOptions) string {
	if p == nil || !opts.active() {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if !p.isAllowedRemoteHost(u.Hostname()) {
		return raw
	}
	q := u.Query()
	if opts.MaxWidth > 0 {
		q.Set("maxWidth", strconv.Itoa(opts.MaxWidth))
	}
	if opts.MaxHeight > 0 {
		q.Set("maxHeight", strconv.Itoa(opts.MaxHeight))
	}
	q.Set("quality", strconv.Itoa(opts.encodingQuality()))
	u.RawQuery = q.Encode()
	return u.String()
}
