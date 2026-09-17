package hd2

import "time"

// SnapshotStore 是快照存储接口，由 state.Store 实现；定义为接口便于测试注入。
type SnapshotStore interface {
	// PutSnapshot 保存某类数据的最新原始 JSON 与抓取时间。
	PutSnapshot(name string, data []byte, at time.Time) error
	// GetSnapshot 读取快照；不存在时 ok 为 false。
	GetSnapshot(name string) (data []byte, fetchedAt time.Time, ok bool, err error)
}
