package importurl

import "net/url"

func Build(subscriptionURL, name string) string {
	return "sing-box://import-remote-profile?url=" + url.QueryEscape(subscriptionURL) + "#" + url.PathEscape(name)
}
