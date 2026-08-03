// Package protocol defines the iLink Bot wire shapes (JSON over HTTP).
//
// Types are pure wire mirrors of the WeixinMessage / CGI contracts. Field
// comments note only non-obvious protocol quirks (e.g. key encodings).
package protocol

import "encoding/json"

// Path* are CGI paths relative to the bot base URL.
const (
	PathGetUpdates   = "ilink/bot/getupdates"
	PathSendMessage  = "ilink/bot/sendmessage"
	PathGetUploadURL = "ilink/bot/getuploadurl"
	PathGetConfig    = "ilink/bot/getconfig"
	PathSendTyping   = "ilink/bot/sendtyping"
	PathNotifyStart  = "ilink/bot/msg/notifystart"
	PathNotifyStop   = "ilink/bot/msg/notifystop"
)

// AuthorizationTypeILinkBotToken is the AuthorizationType header value for bot CGI.
const (
	AuthorizationTypeILinkBotToken = "ilink_bot_token"
)

// Wire enums (proto-aligned numbers) for message, item, media, and typing.
const (
	MessageTypeNone = 0
	MessageTypeUser = 1
	MessageTypeBot  = 2

	MessageStateNew        = 0
	MessageStateGenerating = 1
	MessageStateFinish     = 2

	ItemTypeNone           = 0
	ItemTypeText           = 1
	ItemTypeImage          = 2
	ItemTypeVoice          = 3
	ItemTypeFile           = 4
	ItemTypeVideo          = 5
	ItemTypeToolCallStart  = 11
	ItemTypeToolCallResult = 12

	UploadMediaImage = 1
	UploadMediaVideo = 2
	UploadMediaFile  = 3
	UploadMediaVoice = 4

	TypingStatusTyping = 1
	TypingStatusCancel = 2

	// StaleTokenErrCode is returned when the bot token is expired/stale.
	StaleTokenErrCode = -14
)

// BaseInfo is attached to every CGI body for observability.
type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
	BotAgent       string `json:"bot_agent,omitempty"`
}

// CDNMedia is a CDN reference; AES key is base64 in JSON for media.aes_key.
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

// TextItem is a wire text payload.
type TextItem struct {
	Text string `json:"text,omitempty"`
}

// ImageItem is a wire image payload.
type ImageItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	ThumbMedia *CDNMedia `json:"thumb_media,omitempty"`
	AESKey     string    `json:"aeskey,omitempty"` // hex preferred on inbound
	URL        string    `json:"url,omitempty"`
	MidSize    int       `json:"mid_size,omitempty"`
	ThumbSize  int       `json:"thumb_size,omitempty"`
	HDSize     int       `json:"hd_size,omitempty"`
}

// VoiceItem is a wire voice payload.
type VoiceItem struct {
	Media         *CDNMedia `json:"media,omitempty"`
	EncodeType    int       `json:"encode_type,omitempty"`
	BitsPerSample int       `json:"bits_per_sample,omitempty"`
	SampleRate    int       `json:"sample_rate,omitempty"`
	Playtime      int       `json:"playtime,omitempty"`
	Text          string    `json:"text,omitempty"`
}

// FileItem is a wire file attachment payload.
type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

// VideoItem is a wire video payload.
type VideoItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	VideoSize   int       `json:"video_size,omitempty"`
	PlayLength  int       `json:"play_length,omitempty"`
	VideoMD5    string    `json:"video_md5,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	ThumbSize   int       `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
}

// RefMessage is defined in ref.go (flexible JSON unmarshaling).

// ToolCallStartItem is a wire tool-call start progress item.
type ToolCallStartItem struct {
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCallResultItem is a wire tool-call result progress item.
type ToolCallResultItem struct {
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Status     string `json:"status,omitempty"`
}

// MessageItem is one content unit inside a WeixinMessage item_list.
type MessageItem struct {
	Type               int                 `json:"type,omitempty"`
	CreateTimeMs       int64               `json:"create_time_ms,omitempty"`
	UpdateTimeMs       int64               `json:"update_time_ms,omitempty"`
	IsCompleted        bool                `json:"is_completed,omitempty"`
	MsgID              string              `json:"msg_id,omitempty"`
	RefMsg             *RefMessage         `json:"ref_msg,omitempty"`
	TextItem           *TextItem           `json:"text_item,omitempty"`
	ImageItem          *ImageItem          `json:"image_item,omitempty"`
	VoiceItem          *VoiceItem          `json:"voice_item,omitempty"`
	FileItem           *FileItem           `json:"file_item,omitempty"`
	VideoItem          *VideoItem          `json:"video_item,omitempty"`
	ToolCallStartItem  *ToolCallStartItem  `json:"tool_call_start_item,omitempty"`
	ToolCallResultItem *ToolCallResultItem `json:"tool_call_result_item,omitempty"`
	// Present on some ref_msg.message_item shells from production getupdates.
	ButtonItemList    []json.RawMessage `json:"button_item_list,omitempty"`
	AtBotUsernameList []string          `json:"at_bot_username_list,omitempty"`
}

// WeixinMessage is the unified message envelope on the wire.
type WeixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMs int64         `json:"create_time_ms,omitempty"`
	UpdateTimeMs int64         `json:"update_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	GroupID      string        `json:"group_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
	RunID        string        `json:"run_id,omitempty"`
}

// GetUpdatesReq is the getupdates request body.
type GetUpdatesReq struct {
	GetUpdatesBuf string    `json:"get_updates_buf"`
	BaseInfo      *BaseInfo `json:"base_info,omitempty"`
}

// GetUpdatesResp is the getupdates response body.
type GetUpdatesResp struct {
	Ret                  int             `json:"ret,omitempty"`
	ErrCode              int             `json:"errcode,omitempty"`
	ErrMsg               string          `json:"errmsg,omitempty"`
	Msgs                 []WeixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string          `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMs int             `json:"longpolling_timeout_ms,omitempty"`
}

// SendMessageReq is the sendmessage request body.
type SendMessageReq struct {
	Msg      *WeixinMessage `json:"msg,omitempty"`
	BaseInfo *BaseInfo      `json:"base_info,omitempty"`
}

// SendMessageResp is the sendmessage response body.
type SendMessageResp struct {
	Ret    int    `json:"ret,omitempty"`
	ErrMsg string `json:"errmsg,omitempty"`
}

// GetUploadURLReq is the getuploadurl request body.
type GetUploadURLReq struct {
	FileKey         string    `json:"filekey,omitempty"`
	MediaType       int       `json:"media_type,omitempty"`
	ToUserID        string    `json:"to_user_id,omitempty"`
	RawSize         int       `json:"rawsize,omitempty"`
	RawFileMD5      string    `json:"rawfilemd5,omitempty"`
	FileSize        int       `json:"filesize,omitempty"`
	ThumbRawSize    int       `json:"thumb_rawsize,omitempty"`
	ThumbRawFileMD5 string    `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize   int       `json:"thumb_filesize,omitempty"`
	NoNeedThumb     bool      `json:"no_need_thumb,omitempty"`
	AESKey          string    `json:"aeskey,omitempty"`
	BaseInfo        *BaseInfo `json:"base_info,omitempty"`
}

// GetUploadURLResp is the getuploadurl response body.
type GetUploadURLResp struct {
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
}

// GetConfigReq is the getconfig request body.
type GetConfigReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	ContextToken string    `json:"context_token,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

// GetConfigResp is the getconfig response body.
type GetConfigResp struct {
	Ret          int    `json:"ret,omitempty"`
	ErrMsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

// SendTypingReq is the sendtyping request body.
type SendTypingReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	TypingTicket string    `json:"typing_ticket,omitempty"`
	Status       int       `json:"status,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

// NotifyReq is the notifystart/notifystop request body.
type NotifyReq struct {
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

// NotifyResp is the notifystart/notifystop response body.
type NotifyResp struct {
	Ret    int    `json:"ret,omitempty"`
	ErrMsg string `json:"errmsg,omitempty"`
}
