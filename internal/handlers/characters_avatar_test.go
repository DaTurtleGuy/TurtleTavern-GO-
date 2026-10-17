package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
)

// multipartAvatarRequest builds a character-form request carrying one 'avatar'
// part. filename/ctype model what a browser sends; body is the part content.
func multipartAvatarRequest(t *testing.T, filename, ctype string, body []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	disposition := `form-data; name="avatar"; filename="` + filename + `"`
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", disposition)
	hdr.Set("Content-Type", ctype)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/characters/edit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	return req
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

func isPNG(data []byte) bool {
	return len(data) > 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n"))
}

// A JPEG avatar (what ST's ensureImageFormatSupported lets through untouched)
// must come back as PNG. Before the fix it stayed a JPEG, the card writer
// failed on it, and the handler answered 200 with the picture unchanged.
func TestReadUploadedAvatarNormalizesJPEG(t *testing.T) {
	req := multipartAvatarRequest(t, "images.jpg", "image/jpeg", jpegBytes(t, 24, 32))
	got, err := readUploadedAvatar(req)
	if err != nil {
		t.Fatalf("readUploadedAvatar(JPEG) error: %v", err)
	}
	if !isPNG(got) {
		t.Fatalf("JPEG avatar not re-encoded to PNG (first bytes % x)", got[:min(8, len(got))])
	}
	// The result must be a real character-card image, i.e. decodable.
	if _, err := png.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("re-encoded avatar is not decodable PNG: %v", err)
	}
}

func TestReadUploadedAvatarPassesPNGThrough(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	req := multipartAvatarRequest(t, "avatar.png", "image/png", buf.Bytes())
	got, err := readUploadedAvatar(req)
	if err != nil {
		t.Fatalf("readUploadedAvatar(PNG) error: %v", err)
	}
	if !isPNG(got) {
		t.Fatal("PNG avatar did not stay PNG")
	}
}

// An empty file part means "no new picture" — it must NOT be treated as an
// image, because a nil image makes the writer substitute the default avatar.
func TestReadUploadedAvatarEmptyPartMeansNoImage(t *testing.T) {
	req := multipartAvatarRequest(t, "images.jpg", "image/jpeg", nil)
	got, err := readUploadedAvatar(req)
	if err != nil {
		t.Fatalf("readUploadedAvatar(empty) error: %v", err)
	}
	if got != nil {
		t.Fatalf("empty avatar part should mean no image, got %d bytes", len(got))
	}
}

// No file part at all (a plain form edit) is also "no new picture".
func TestReadUploadedAvatarMissingPartMeansNoImage(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("ch_name", "Someone"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/characters/edit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	got, err := readUploadedAvatar(req)
	if err != nil {
		t.Fatalf("readUploadedAvatar(no part) error: %v", err)
	}
	if got != nil {
		t.Fatalf("missing avatar part should mean no image, got %d bytes", len(got))
	}
}

// Bytes that are not an image at all must be reported, never silently ignored.
func TestReadUploadedAvatarRejectsGarbage(t *testing.T) {
	req := multipartAvatarRequest(t, "notes.txt", "text/plain",
		[]byte("this is definitely not an image"))
	if _, err := readUploadedAvatar(req); err == nil {
		t.Fatal("expected an error for a non-image upload")
	}
}

// A zero-byte PNG-extension upload with a real image in the card must not be
// able to blank the card: covered by the empty-part test above plus this
// explicit check that we never return a non-nil zero-length slice.
func TestReadUploadedAvatarNeverReturnsEmptySlice(t *testing.T) {
	for _, body := range [][]byte{nil, {}} {
		req := multipartAvatarRequest(t, "x.png", "image/png", body)
		got, err := readUploadedAvatar(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil && len(got) == 0 {
			t.Fatal("returned a non-nil zero-length image; that would clobber the card")
		}
	}
}
