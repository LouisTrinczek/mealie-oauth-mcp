package store

import "net/url"

func fileDSN(path, query string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + query
}
