package character

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/util"
)

type pngChunk struct {
	typ  [4]byte
	data []byte
}

var pngSignature = []byte{137, 80, 78, 71, 13, 10, 26, 10}

func parsePNGChunks(data []byte) ([]pngChunk, error) {
	if len(data) < 8 || !bytes.Equal(data[:8], pngSignature) {
		return nil, io.ErrUnexpectedEOF
	}
	pos := 8
	var chunks []pngChunk
	for pos+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		pos += 4
		var typ [4]byte
		copy(typ[:], data[pos:pos+4])
		pos += 4
		if length < 0 || pos+length+4 > len(data) {
			break
		}
		chunkData := make([]byte, length)
		copy(chunkData, data[pos:pos+length])
		pos += length + 4
		chunks = append(chunks, pngChunk{typ: typ, data: chunkData})
	}
	return chunks, nil
}

func encodePNGChunks(chunks []pngChunk) []byte {
	var buf bytes.Buffer
	buf.Write(pngSignature)
	for _, c := range chunks {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(c.data)))
		buf.Write(lenBuf[:])
		buf.Write(c.typ[:])
		buf.Write(c.data)
		crc := crc32.NewIEEE()
		crc.Write(c.typ[:])
		crc.Write(c.data)
		var crcBuf [4]byte
		binary.BigEndian.PutUint32(crcBuf[:], crc.Sum32())
		buf.Write(crcBuf[:])
	}
	return buf.Bytes()
}

func decodePNGTextData(rawData []byte) (keyword string, text string, ok bool) {
	nulIdx := bytes.IndexByte(rawData, 0)
	if nulIdx < 0 {
		return "", "", false
	}
	keyword = string(rawData[:nulIdx])
	text = string(rawData[nulIdx+1:])
	return keyword, text, true
}

func encodePNGTextChunk(keyword, text string) pngChunk {
	rawData := make([]byte, len(keyword)+1+len(text))
	copy(rawData, keyword)
	rawData[len(keyword)] = 0
	copy(rawData[len(keyword)+1:], text)
	return pngChunk{typ: [4]byte{'t', 'E', 'X', 't'}, data: rawData}
}

func ReadCharacterDataFromPNG(imageData []byte) (string, error) {
	chunks, err := parsePNGChunks(imageData)
	if err != nil {
		return "", err
	}
	var charaText, ccv3Text string
	for _, c := range chunks {
		if string(c.typ[:]) != "tEXt" {
			continue
		}
		keyword, text, ok := decodePNGTextData(c.data)
		if !ok {
			continue
		}
		lower := strings.ToLower(keyword)
		if lower == "ccv3" {
			ccv3Text = text
		} else if lower == "chara" {
			charaText = text
		}
	}
	if ccv3Text != "" {
		decoded, err := base64.StdEncoding.DecodeString(ccv3Text)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(ccv3Text)
		}
		if err == nil {
			return string(decoded), nil
		}
	}
	if charaText != "" {
		decoded, err := base64.StdEncoding.DecodeString(charaText)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(charaText)
		}
		if err == nil {
			return string(decoded), nil
		}
	}
	return "", io.ErrNoProgress
}

func ReadCharacterDataFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return ReadCharacterDataFromPNG(data)
}

func WriteCharacterDataToPNG(imageData []byte, jsonData string) ([]byte, error) {
	chunks, err := parsePNGChunks(imageData)
	if err != nil {
		return nil, err
	}
	var filtered []pngChunk
	for _, c := range chunks {
		if string(c.typ[:]) != "tEXt" {
			filtered = append(filtered, c)
			continue
		}
		keyword, _, ok := decodePNGTextData(c.data)
		if !ok {
			filtered = append(filtered, c)
			continue
		}
		lower := strings.ToLower(keyword)
		if lower == "chara" || lower == "ccv3" {
			continue
		}
		filtered = append(filtered, c)
	}
	charaEncoded := base64.StdEncoding.EncodeToString([]byte(jsonData))
	charaChunk := encodePNGTextChunk("chara", charaEncoded)
	if len(filtered) > 0 {
		filtered = append(filtered[:len(filtered)-1], charaChunk, filtered[len(filtered)-1])
	} else {
		filtered = append(filtered, charaChunk)
	}
	v3Data := make(map[string]any)
	if err := json.Unmarshal([]byte(jsonData), &v3Data); err == nil {
		v3Data["spec"] = "chara_card_v3"
		v3Data["spec_version"] = "3.0"
		if v3JSON, err := json.Marshal(v3Data); err == nil {
			ccv3Encoded := base64.StdEncoding.EncodeToString(v3JSON)
			ccv3Chunk := encodePNGTextChunk("ccv3", ccv3Encoded)
			idx := len(filtered) - 1
			filtered = append(filtered[:idx:idx], append([]pngChunk{ccv3Chunk}, filtered[idx:]...)...)
		}
	}
	return encodePNGChunks(filtered), nil
}

const (
	AvatarWidth  = 512
	AvatarHeight = 768
)

var DefaultAvatarPNG []byte

func init() {
	candidates := []string{
		util.ResolveAppPath(filepath.Join("public", "img", "ai4.png")),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil && len(data) > 0 {
			DefaultAvatarPNG = data
			return
		}
	}
	DefaultAvatarPNG = createMinimalPNG()
}

// ReloadDefaultAvatar re-resolves the default avatar against a specific public
// directory. Mobile hosts set their app directory after package init has run.
func ReloadDefaultAvatar(publicDir string) {
	if data, err := os.ReadFile(filepath.Join(publicDir, "img", "ai4.png")); err == nil && len(data) > 0 {
		DefaultAvatarPNG = data
	}
}

func createMinimalPNG() []byte {
	chunks := []pngChunk{
		{
			typ: [4]byte{'I', 'H', 'D', 'R'},
			data: func() []byte {
				d := make([]byte, 13)
				binary.BigEndian.PutUint32(d[0:4], 1)
				binary.BigEndian.PutUint32(d[4:8], 1)
				d[8] = 8
				d[9] = 2
				d[10] = 0
				d[11] = 0
				d[12] = 0
				return d
			}(),
		},
		{
			typ:  [4]byte{'I', 'D', 'A', 'T'},
			data: CreateZlibCompressedChunk([]byte{0, 0, 0, 0}),
		},
		{
			typ:  [4]byte{'I', 'E', 'N', 'D'},
			data: []byte{},
		},
	}
	return encodePNGChunks(chunks)
}

func CreateZlibCompressedChunk(data []byte) []byte {
	var buf bytes.Buffer
	w, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}
