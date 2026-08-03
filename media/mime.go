package media

import (
	"net/http"
	"path/filepath"
	"strings"
)

var extToMIME = map[string]string{
	".pdf":  "application/pdf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".txt":  "text/plain",
	".csv":  "text/csv",
	".zip":  "application/zip",
	".tar":  "application/x-tar",
	".gz":   "application/gzip",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".wav":  "audio/wav",
	".silk": "audio/silk",
	".mp4":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

var mimeToExt = map[string]string{
	"image/jpeg":      ".jpg",
	"image/jpg":       ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/bmp":       ".bmp",
	"video/mp4":       ".mp4",
	"video/quicktime": ".mov",
	"video/webm":      ".webm",
	"audio/mpeg":      ".mp3",
	"audio/ogg":       ".ogg",
	"audio/wav":       ".wav",
	"audio/silk":      ".silk",
	"application/pdf": ".pdf",
	"application/zip": ".zip",
	"text/plain":      ".txt",
}

// MIMEFromFilename returns a MIME type from extension, or application/octet-stream.
func MIMEFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if m, ok := extToMIME[ext]; ok {
		return m
	}
	return "application/octet-stream"
}

// ExtFromMIME returns a file extension for a content-type (including parameters).
func ExtFromMIME(mimeType string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if e, ok := mimeToExt[ct]; ok {
		return e
	}
	return ".bin"
}

// DetectMediaKind classifies a local path for outbound routing.
// Returns image|video|voice|file.
func DetectMediaKind(path string) string {
	mime := MIMEFromFilename(path)
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case mime == "audio/silk" || mime == "audio/wav" || mime == "audio/mpeg" || mime == "audio/ogg":
		return "voice"
	default:
		// sniff file header for common cases when extension missing
		if data, err := readHead(path, 512); err == nil {
			ct := http.DetectContentType(data)
			if strings.HasPrefix(ct, "image/") {
				return "image"
			}
			if strings.HasPrefix(ct, "video/") {
				return "video"
			}
			if strings.HasPrefix(ct, "audio/") {
				return "voice"
			}
		}
		return "file"
	}
}

func readHead(path string, n int) ([]byte, error) {
	// small helper without importing session
	f, err := openRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	nr, err := f.Read(buf)
	if nr > 0 {
		return buf[:nr], nil
	}
	return nil, err
}
