// Package state 用一个 bbolt 文件保存数据快照、推送去重状态与元数据。
// 单群机器人不需要数据库，这个小文件足以保证「重启不重复推、不漏推」。
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// 四个桶名：快照、推送去重、元数据、通用键值（S3 起翻译缓存用它）。
var (
	bucketSnapshot = []byte("snapshot")
	bucketPush     = []byte("pushstate")
	bucketMeta     = []byte("meta")
	bucketKV       = []byte("kv")
)

// openTimeout 是等待文件锁的最长时间；超时说明文件被其它进程占用。
// 声明为变量是为了让测试能改小，避免用例白等 3 秒。
var openTimeout = 3 * time.Second

// Store 封装 bbolt 数据库，所有方法都可以并发调用（bbolt 内部串行化读写事务）。
type Store struct {
	db *bolt.DB
}

// payload 是快照在桶里的存储格式：原始 JSON 与抓取时间。
type payload struct {
	Data      json.RawMessage `json:"data"`
	FetchedAt time.Time       `json:"fetchedAt"`
}

// Open 打开（必要时创建）状态文件，父目录会自动创建。
// bbolt 同一文件只能被一个进程打开，被占用时会等待 openTimeout 后返回错误。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建状态目录失败：%w", err)
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, fmt.Errorf("打开状态文件 %s 失败（可能已被其它进程占用）：%w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketSnapshot, bucketPush, bucketMeta, bucketKV} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化状态桶失败：%w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭状态文件，释放文件锁。
func (s *Store) Close() error { return s.db.Close() }

// PutSnapshot 保存某类数据的最新原始 JSON 与抓取时间。
// data 必须是合法 JSON（否则返回错误且不写入），时间统一按 UTC 存储。
func (s *Store) PutSnapshot(name string, data []byte, at time.Time) error {
	raw, err := json.Marshal(payload{Data: data, FetchedAt: at.UTC()})
	if err != nil {
		return fmt.Errorf("序列化快照失败：%w", err)
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSnapshot).Put([]byte(name), raw)
	})
	if err != nil {
		return fmt.Errorf("写入快照 %s 失败：%w", name, err)
	}
	return nil
}

// GetSnapshot 读取快照；不存在时 ok 为 false。
// 返回的 data 是副本，调用方改写不会影响已存储的数据。
func (s *Store) GetSnapshot(name string) (data []byte, fetchedAt time.Time, ok bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketSnapshot).Get([]byte(name))
		if raw == nil {
			return nil
		}
		var p payload
		if e := json.Unmarshal(raw, &p); e != nil {
			return fmt.Errorf("解析失败：%w", e)
		}
		data, fetchedAt, ok = p.Data, p.FetchedAt, true
		return nil
	})
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("读取快照 %s 失败：%w", name, err)
	}
	return data, fetchedAt, ok, nil
}

// MarkPushed 记录某个事件已推送；首次记录返回 true，重复记录返回 false。
// kind 用于区分事件类型（例如 dispatch、event），id 是事件自身的唯一标识；
// 两者拼接成桶内的键，因此都不允许包含冒号。重复记录不会覆盖首次写入的时间戳。
func (s *Store) MarkPushed(kind, id string, at time.Time) (bool, error) {
	key := []byte(kind + ":" + id)
	first := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPush)
		if b.Get(key) != nil {
			return nil
		}
		first = true
		return b.Put(key, []byte(at.UTC().Format(time.RFC3339)))
	})
	if err != nil {
		return false, fmt.Errorf("写入推送状态失败：%w", err)
	}
	return first, nil
}

// HasPushed 判断某个事件是否已推送过。
func (s *Store) HasPushed(kind, id string) (bool, error) {
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(bucketPush).Get([]byte(kind+":"+id)) != nil
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("读取推送状态 %s:%s 失败：%w", kind, id, err)
	}
	return found, nil
}

// SetMeta 写入元数据（例如上次轮询时间）；同 key 会被覆盖。
func (s *Store) SetMeta(key, value string) error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMeta).Put([]byte(key), []byte(value))
	})
	if err != nil {
		return fmt.Errorf("写入元数据 %s 失败：%w", key, err)
	}
	return nil
}

// GetMeta 读取元数据；不存在时 ok 为 false。
func (s *Store) GetMeta(key string) (string, bool, error) {
	var value string
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketMeta).Get([]byte(key))
		if raw == nil {
			return nil
		}
		value, found = string(raw), true
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("读取元数据 %s 失败：%w", key, err)
	}
	return value, found, nil
}

// PutKV 写入一条通用键值记录（kind + key 相同则覆盖）。
// kind 用于分域（S3 起翻译缓存用 "translate"），key 是域内的键；两者一起拼成桶内键。
// kind 与 key 去除首尾空白后都不能为空（纯空白视为空），且都不允许包含冒号——
// 否则 (a:b, c) 与 (a, b:c) 会撞成同一条记录。
func (s *Store) PutKV(kind, key, value string) error {
	if err := checkKVKey(kind, key); err != nil {
		return fmt.Errorf("校验键值 %s:%s 失败：%w", kind, key, err)
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketKV).Put([]byte(kind+":"+key), []byte(value))
	})
	if err != nil {
		return fmt.Errorf("写入键值 %s:%s 失败：%w", kind, key, err)
	}
	return nil
}

// GetKV 读取一条通用键值记录；不存在时 ok 为 false（缺键不算错误）。
// kind 与 key 的取值要求同 PutKV：去除首尾空白后都不能为空，且都不允许包含冒号；
// 参数不合格时返回错误，而不是被当成「没查到」。
// 返回的字符串是 bbolt 页数据的拷贝，调用方长期持有它不会延长事务的生命周期。
func (s *Store) GetKV(kind, key string) (value string, ok bool, err error) {
	if err := checkKVKey(kind, key); err != nil {
		return "", false, fmt.Errorf("校验键值 %s:%s 失败：%w", kind, key, err)
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketKV).Get([]byte(kind + ":" + key))
		if raw == nil {
			return nil
		}
		value, ok = string(raw), true
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("读取键值 %s:%s 失败：%w", kind, key, err)
	}
	return value, ok, nil
}

// checkKVKey 校验 kind 与 key：去掉首尾空白（TrimSpace）后都不能为空，且都不允许包含冒号。
func checkKVKey(kind, key string) error {
	if strings.TrimSpace(kind) == "" {
		return errors.New("键值 kind 不能为空")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("键值 key 不能为空")
	}
	if strings.Contains(kind, ":") || strings.Contains(key, ":") {
		return fmt.Errorf("键值的 kind 与 key 都不能包含冒号（kind=%q key=%q）", kind, key)
	}
	return nil
}
