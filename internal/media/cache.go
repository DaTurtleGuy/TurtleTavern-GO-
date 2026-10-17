package media

import "net/http"

// SetRevalidateHeader marks a response as "store it, but revalidate before
// every reuse".
//
// The Node backend serves user data files through Express's res.sendFile,
// which sends "Cache-Control: public, max-age=0" (plus an ETag). The Go port
// used plain http.ServeFile and sent no Cache-Control at all. A response with
// no freshness information is cached heuristically by browsers: Chrome (and
// therefore the Android WebView) keeps serving it from the HTTP cache for a
// fraction of the file's age without ever asking the server again.
//
// Every path that serves a user image serves a file that is overwritten IN
// PLACE — a persona/character picture keeps its file name, only its bytes
// change. Heuristic caching makes those overwrites invisible: the picture
// "does not update" for days even though the server already has (and serves)
// the new bytes. Express's default keeps the browser in check; the Go port has
// to ask for the same explicitly.
//
// The server-side half of this problem (dropping the stale generated thumbnail
// when the original image changes) is handled by InvalidateThumbnail.
func SetRevalidateHeader(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=0")
}
