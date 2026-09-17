package hd2

import (
	"errors"
	"fmt"
)

// ErrRateLimited 表示上游限流冷却中，调用方应提示用户稍后再试，不要立即重试。
var ErrRateLimited = errors.New("上游接口限流中")

// statusError 表示上游返回了非 2xx 状态码。
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("上游返回状态码 %d：%s", e.code, e.body)
}
