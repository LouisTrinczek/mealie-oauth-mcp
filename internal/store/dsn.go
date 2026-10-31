package store

import "net/url"

func fileDSN(path, query string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
}
