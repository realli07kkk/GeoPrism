package render

import (
	"encoding/json"
	"io"
)

// OutputMode 输出模式
type OutputMode int

const (
	// OutputText 文本模式（默认）
	OutputText OutputMode = iota
	// OutputJSON JSON 模式
	OutputJSON
)

// WriteJSON 统一 JSON 输出
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
