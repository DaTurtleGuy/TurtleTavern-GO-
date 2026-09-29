// Package remotebackup is the client half of the TurtleTavern Backupper
// protocol (v1). The wire types mirror the Backupper's internal/protocol
// package and PROTOCOL.md; the two must stay in sync.
package remotebackup

// ProtocolVersion is the wire protocol this client speaks.
const ProtocolVersion = 1

// Entry is one file inside a snapshot manifest. Hash is the lowercase hex
// SHA-256 of the file's uncompressed bytes.
type Entry struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode,omitempty"`
	ModTime string `json:"modTime,omitempty"`
}

// Meta describes a snapshot. The server assigns ID and ReceivedAt.
type Meta struct {
	Label          string `json:"label,omitempty"`
	Device         string `json:"device,omitempty"`
	AppVersion     string `json:"appVersion,omitempty"`
	Handle         string `json:"handle,omitempty"`
	IncludeKeys    bool   `json:"includeKeys,omitempty"`
	IncludeBackups bool   `json:"includeBackups,omitempty"`

	ID           string `json:"id,omitempty"`
	ReceivedAt   string `json:"receivedAt,omitempty"`
	FileCount    int    `json:"fileCount"`
	LogicalBytes int64  `json:"logicalBytes"`
}

// SnapshotInfo is a snapshot summary as returned by the list endpoint.
type SnapshotInfo struct {
	ID           string `json:"id"`
	ReceivedAt   string `json:"receivedAt"`
	Label        string `json:"label,omitempty"`
	Device       string `json:"device,omitempty"`
	AppVersion   string `json:"appVersion,omitempty"`
	Handle       string `json:"handle,omitempty"`
	FileCount    int    `json:"fileCount"`
	LogicalBytes int64  `json:"logicalBytes"`
	StoredBytes  int64  `json:"storedBytes"`
	SharedBytes  int64  `json:"sharedBytes"`
}

type CheckRequest struct {
	Hashes []string `json:"hashes"`
}

type CheckResponse struct {
	Missing []string `json:"missing"`
}

type PutFileResponse struct {
	Hash         string `json:"hash"`
	Size         int64  `json:"size"`
	ChunksNew    int    `json:"chunksNew"`
	ChunksReused int    `json:"chunksReused"`
	StoredBytes  int64  `json:"storedBytes"`
}

type CreateSnapshotRequest struct {
	Meta    Meta    `json:"meta"`
	Entries []Entry `json:"entries"`
}

type CreateSnapshotResponse struct {
	Snapshot  SnapshotInfo `json:"snapshot"`
	Duplicate bool         `json:"duplicate"`
}

type SnapshotListResponse struct {
	Snapshots []SnapshotInfo `json:"snapshots"`
}

type SnapshotResponse struct {
	Meta    Meta    `json:"meta"`
	Entries []Entry `json:"entries"`
}

type Capabilities struct {
	Protocol      int    `json:"protocol"`
	Version       string `json:"version"`
	ChunkAlgo     string `json:"chunkAlgo"`
	MaxFileBytes  int64  `json:"maxFileBytes"`
	MaxTotalBytes int64  `json:"maxTotalBytes"`
	StoredBytes   int64  `json:"storedBytes"`
	Snapshots     int    `json:"snapshots"`
}

type Health struct {
	OK       bool   `json:"ok"`
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
