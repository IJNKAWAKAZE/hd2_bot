package hd2

import (
	"context"
	"time"
)

// TTLConfig 是各类数据的缓存时长；零值表示不缓存（每次查询都打上游）。
type TTLConfig struct {
	War         time.Duration
	Planets     time.Duration
	Campaigns   time.Duration
	Assignments time.Duration
	Dispatches  time.Duration
	Events      time.Duration
	Stations    time.Duration
}

// Result 是一次查询的结果：数据 + 数据时间 + 是否来自快照降级。
// FetchedAt 是「这份数据是什么时候抓到的」：命中缓存时是当初抓取的时间，
// 降级时是快照的写入时间，都不是本次请求的时间。
// Stale 为 true 时展示层必须提示「数据可能已过期」。
type Result[T any] struct {
	Value     T
	FetchedAt time.Time
	Stale     bool
}

// Service 是数据层对上层暴露的唯一入口，统一负责缓存、限流与快照降级。
type Service struct {
	client    *Client
	companion *Companion
	cache     *Cache
	store     SnapshotStore
	ttl       TTLConfig
	now       func() time.Time
	logf      func(string, ...any)
}

// ServiceConfig 是组装 Service 所需的依赖。
type ServiceConfig struct {
	Client    *Client
	Companion *Companion
	Store     SnapshotStore // 为 nil 时不做快照降级
	TTL       TTLConfig
	Now       func() time.Time // 为 nil 时使用系统时钟；缓存过期判定与快照时间共用它
	Logger    func(format string, args ...any)
}

// NewService 创建数据服务。
func NewService(cfg ServiceConfig) *Service {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logf := cfg.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	cache := NewCache()
	// 让缓存过期判定跟随注入的时钟，便于测试用假时钟推进时间。
	cache.now = now
	return &Service{
		client:    cfg.Client,
		companion: cfg.Companion,
		cache:     cache,
		store:     cfg.Store,
		ttl:       cfg.TTL,
		now:       now,
		logf:      logf,
	}
}

// Companion 返回补充源客户端，供后续子项目直接使用；未配置时返回 nil。
func (s *Service) Companion() *Companion { return s.companion }

// War 返回银河战况。返回值是副本，调用方可以随意修改，不会影响缓存里的数据。
func (s *Service) War(ctx context.Context) (Result[*War], error) {
	res, err := fetch(s, ctx, string(EndpointWar), s.ttl.War, DecodeWar)
	if res.Value != nil {
		v := *res.Value
		res.Value = &v
	}
	return res, err
}

// Planets 返回星球列表。
func (s *Service) Planets(ctx context.Context) (Result[[]Planet], error) {
	res, err := fetch(s, ctx, string(EndpointPlanets), s.ttl.Planets, DecodePlanets)
	res.Value = cloneSlice(res.Value)
	return res, err
}

// Campaigns 返回进行中的战役。
func (s *Service) Campaigns(ctx context.Context) (Result[[]Campaign], error) {
	res, err := fetch(s, ctx, string(EndpointCampaigns), s.ttl.Campaigns, DecodeCampaigns)
	res.Value = cloneSlice(res.Value)
	return res, err
}

// Assignments 返回重要指令；当前没有进行中的指令时为空切片。
func (s *Service) Assignments(ctx context.Context) (Result[[]Assignment], error) {
	res, err := fetch(s, ctx, string(EndpointAssignments), s.ttl.Assignments, DecodeAssignments)
	res.Value = cloneSlice(res.Value)
	return res, err
}

// Dispatches 返回战役简报。
func (s *Service) Dispatches(ctx context.Context) (Result[[]Dispatch], error) {
	res, err := fetch(s, ctx, string(EndpointDispatches), s.ttl.Dispatches, DecodeDispatches)
	res.Value = cloneSlice(res.Value)
	return res, err
}

// Events 返回星球事件。
func (s *Service) Events(ctx context.Context) (Result[[]PlanetEvent], error) {
	res, err := fetch(s, ctx, string(EndpointEvents), s.ttl.Events, DecodeEvents)
	res.Value = cloneSlice(res.Value)
	return res, err
}

// Stations 返回民主空间站。
func (s *Service) Stations(ctx context.Context) (Result[[]SpaceStation], error) {
	res, err := fetch(s, ctx, string(EndpointStations), s.ttl.Stations, DecodeStations)
	res.Value = cloneSlice(res.Value)
	return res, err
}

// cloneSlice 复制一层切片，避免调用方就地排序或改写元素时污染缓存里的数据。
// 只复制一层：元素内部的指针字段（例如 Planet.Event）仍与缓存共享，调用方不要改动它们。
func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

// fetch 是各查询方法的统一实现：命中缓存直接返回；否则经限流取数并写快照；
// 取数（或解码）失败时回落到最近一次快照，并标记 Stale。
func fetch[T any](s *Service, ctx context.Context, name string, ttl time.Duration, decode func([]byte) (T, error)) (Result[T], error) {
	var zero Result[T]
	value, err := Fetch(s.cache, ctx, name, ttl, func(ctx context.Context) (T, error) {
		var empty T
		body, err := s.client.Get(ctx, Endpoint(name))
		if err != nil {
			return empty, err
		}
		decoded, err := decode(body)
		if err != nil {
			return empty, err
		}
		if s.store != nil {
			if err := s.store.PutSnapshot(name, body, s.now()); err != nil {
				// 快照只是降级用的备份，写失败不影响本次查询。
				s.logf("写入快照失败 name=%s err=%v", name, err)
			}
		}
		return decoded, nil
	})
	if err == nil {
		at := s.now()
		if cachedAt, ok := s.cache.FetchedAt(name); ok {
			at = cachedAt
		}
		return Result[T]{Value: value, FetchedAt: at}, nil
	}
	if s.store == nil {
		return zero, err
	}

	body, fetchedAt, ok, storeErr := s.store.GetSnapshot(name)
	if storeErr != nil {
		s.logf("读取快照失败 name=%s err=%v", name, storeErr)
		return zero, err
	}
	if !ok {
		return zero, err
	}
	decoded, decodeErr := decode(body)
	if decodeErr != nil {
		s.logf("快照解析失败 name=%s err=%v", name, decodeErr)
		return zero, err
	}
	s.logf("上游不可用，降级使用快照 name=%s fetched_at=%s", name, fetchedAt.Format(time.RFC3339))
	return Result[T]{Value: decoded, FetchedAt: fetchedAt, Stale: true}, nil
}
