package handlers

import "strings"

// isRemoteMediaProviderPath reports whether path is owned by a remote library
// provider. These paths must be opened through the composite streaming provider;
// the local /webdav endpoint is backed only by the NZB filesystem.
func isRemoteMediaProviderPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	path = strings.TrimLeft(path, "/")
	return strings.HasPrefix(path, "plexmedia:") || strings.HasPrefix(path, "jellyfinmedia:")
}
