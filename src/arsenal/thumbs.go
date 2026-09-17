// 缩略图地址缓存（data/arsenal/thumbs.json）的序列化。
//
// 单独一个文件、只做 JSON 读写：这两个函数是「缓存格式」的唯一入口，
// 单测里注入坏 JSON 就能覆盖「缓存被写坏」这条分支，不必真去写文件。
package arsenal

import "encoding/json"

// parseThumbCache 解析缩略图缓存；内容坏了按空缓存处理（顶多多问几次上游）。
// 解析结果只保留「键与值都非空」的条目：半截脏数据留在缓存里会让行内结果带一个 Telegram 取不到的地址。
func parseThumbCache(raw []byte) map[string]string {
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(parsed))
	for key, value := range parsed {
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

// marshalThumbCache 序列化缩略图缓存。map 为空时也照常输出 `{}`，不留空文件。
func marshalThumbCache(cache map[string]string) ([]byte, error) {
	if cache == nil {
		cache = map[string]string{}
	}
	return json.Marshal(cache)
}
