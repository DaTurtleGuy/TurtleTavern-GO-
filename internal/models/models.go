package models

// VersionInfo represents the response payload for the /version endpoint.
type VersionInfo struct {
	Agent       string `json:"agent"`
	PkgVersion  string `json:"pkgVersion"`
	GitRevision string `json:"gitRevision"`
	GitBranch   string `json:"gitBranch"`
	CommitDate  string `json:"commitDate"`
	IsLatest    bool   `json:"isLatest"`
}

// PingResponse is returned by POST /api/ping (empty body, 204).
// Kept as a type marker — the handler writes no body.
type PingResponse struct{}

// ---------- User directory / context types ----------

// UserDirectories holds all filesystem paths for a single user.
type UserDirectories struct {
	Root          string
	Characters    string
	Chats         string
	Groups        string
	GroupChats    string
	Backups       string
	Thumbnails    string
	Worlds        string
	UserImages    string
	User          string
	Avatars       string
	Backgrounds   string
	Uploads       string
	Assets        string
	Extensions    string
	Files         string
}

// UserProfile holds identity info for the current user.
type UserProfile struct {
	Handle  string `json:"handle"`
	Name    string `json:"name"`
	Admin   bool   `json:"admin"`
	Enabled bool   `json:"enabled"`
}

// UserContext bundles directories and profile for handler use.
type UserContext struct {
	Directories UserDirectories
	Profile     UserProfile
}

// UserContextKey is the context key for *UserContext.
type UserContextKey struct{}

// ---------- Character types ----------

// CharacterData is the V2 spec data block inside a character card.
type CharacterData struct {
	Name                    string                 `json:"name"`
	Description             string                 `json:"description"`
	Personality             string                 `json:"personality"`
	Scenario                string                 `json:"scenario"`
	FirstMes                string                 `json:"first_mes"`
	MesExample              string                 `json:"mes_example"`
	CreatorNotes            string                 `json:"creator_notes"`
	SystemPrompt            string                 `json:"system_prompt"`
	PostHistoryInstructions string                 `json:"post_history_instructions"`
	Tags                    []string               `json:"tags"`
	Creator                 string                 `json:"creator"`
	CharacterVersion        string                 `json:"character_version"`
	AlternateGreetings      []string               `json:"alternate_greetings,omitempty"`
	CharacterBook           any                    `json:"character_book,omitempty"`
	Extensions              map[string]any         `json:"extensions"`
}

// CharacterCard is the full Spec V2 character card stored in PNG chunks.
type CharacterCard struct {
	Spec         string         `json:"spec"`
	SpecVersion  string         `json:"spec_version"`
	Name         string         `json:"name,omitempty"`
	Description  string         `json:"description,omitempty"`
	Personality  string         `json:"personality,omitempty"`
	Scenario     string         `json:"scenario,omitempty"`
	FirstMes     string         `json:"first_mes,omitempty"`
	MesExample   string         `json:"mes_example,omitempty"`
	CreatorNotes string         `json:"creatorcomment,omitempty"`
	Avatar       string         `json:"avatar,omitempty"`
	Chat         string         `json:"chat,omitempty"`
	Talkativeness float64       `json:"talkativeness,omitempty"`
	Fav          bool           `json:"fav,omitempty"`
	Tags         any            `json:"tags,omitempty"`
	CreateDate   string         `json:"create_date,omitempty"`
	Data         *CharacterData `json:"data"`
	Extensions   map[string]any `json:"extensions,omitempty"`
}

// ShallowCharacter is the compact representation returned by /all.
type ShallowCharacter struct {
	Shallow       bool        `json:"shallow"`
	Name          string      `json:"name"`
	Avatar        string      `json:"avatar"`
	Chat          string      `json:"chat"`
	Fav           bool        `json:"fav"`
	DateAdded     float64     `json:"date_added"`
	CreateDate    any         `json:"create_date"`
	DateLastChat  float64     `json:"date_last_chat"`
	ChatSize      int64       `json:"chat_size"`
	DataSize      int         `json:"data_size"`
	Tags          []string    `json:"tags"`
	Data          ShallowData `json:"data"`
}

// ShallowData is the nested data block in the shallow character view.
type ShallowData struct {
	Name             string   `json:"name"`
	CharacterVersion string   `json:"character_version"`
	Creator          string   `json:"creator"`
	CreatorNotes     string   `json:"creator_notes"`
	Tags             []string `json:"tags"`
	Extensions       struct {
		Fav   bool   `json:"fav"`
		World string `json:"world,omitempty"`
	} `json:"extensions"`
}

// ---------- Chat types ----------

// ChatMessage is one line of a JSONL chat file.
type ChatMessage struct {
	ChatMetadata  map[string]any `json:"chat_metadata,omitempty"`
	UserName      string         `json:"user_name,omitempty"`
	CharacterName string         `json:"character_name,omitempty"`
	Name          string         `json:"name,omitempty"`
	IsUser        bool           `json:"is_user,omitempty"`
	SendDate      any            `json:"send_date,omitempty"`
	Mes           string         `json:"mes,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
	Swipes        []string       `json:"swipes,omitempty"`
	SwipeID       int            `json:"swipe_id,omitempty"`
	IsSystem      bool           `json:"is_system,omitempty"`
}

// ChatInfo is the metadata returned for a chat file listing.
type ChatInfo struct {
	Match        bool   `json:"match"`
	FileID       string `json:"file_id,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	FileSize     string `json:"file_size,omitempty"`
	ChatItems    int    `json:"chat_items"`
	Mes          string `json:"mes"`
	LastMes      any    `json:"last_mes"`
	ChatMetadata any    `json:"chat_metadata,omitempty"`
	Avatar       string `json:"avatar,omitempty"`
	Group        string `json:"group,omitempty"`
	PreviewMes   string `json:"preview_message,omitempty"`
}

// ---------- Group types ----------

// GroupData is the JSON file stored in the groups directory.
type GroupData struct {
	ID                       string   `json:"id"`
	Name                     string   `json:"name"`
	Members                  []string `json:"members"`
	AvatarURL                string   `json:"avatar_url,omitempty"`
	AllowSelfResponses       bool     `json:"allow_self_responses"`
	ActivationStrategy       int      `json:"activation_strategy"`
	GenerationMode           int      `json:"generation_mode"`
	DisabledMembers          []string `json:"disabled_members,omitempty"`
	Fav                      any      `json:"fav,omitempty"`
	ChatID                   string   `json:"chat_id"`
	Chats                    []string `json:"chats"`
	AutoModeDelay            int      `json:"auto_mode_delay"`
	GenerationModeJoinPrefix string   `json:"generation_mode_join_prefix"`
	GenerationModeJoinSuffix string   `json:"generation_mode_join_suffix"`
	DateAdded                float64  `json:"date_added,omitempty"`
	CreateDate               string   `json:"create_date,omitempty"`
	DateLastChat             float64  `json:"date_last_chat"`
	ChatSize                 int64    `json:"chat_size"`
}

// Crop defines avatar crop parameters from the frontend.
type Crop struct {
	X          int  `json:"x"`
	Y          int  `json:"y"`
	Width      int  `json:"width"`
	Height     int  `json:"height"`
	WantResize bool `json:"want_resize"`
}

// DateAddedMap is the {name → timestamp} mapping persisted as date_added.json.
type DateAddedMap map[string]float64
