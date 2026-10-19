package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/NATIVE/NATIVE/internal/lessonpipe"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
)

// levelGuide — указания уровня из справочника CEFR (llm_service
// /v1/levels/guidance) для голосового разговора, с небольшим кешем.
//
// Кеш, а не запрос на каждую сессию: указания зависят только от языка и
// уровня, а голосовые сессии в школьный час открывает весь класс разом.
// Кеш ограничен по числу ключей и по времени — справочник обновляется
// выкатом llm_service, и через TTL новые указания дойдут сами. Это кеш
// справочных данных, а не состояние: вторая реплика API со своим кешем
// отдаст то же самое.
//
// Сбой справочника голосовой разговор не останавливает: указаний нет —
// сессия держит уровень по одной строке «уровень A2», как раньше. Неудача
// кешируется ненадолго, чтобы лежащий llm_service не задерживал каждое
// открытие сессии на таймаут запроса.
type levelGuide struct {
	fetch   func(ctx context.Context, language, level string) (string, error)
	ttl     time.Duration
	failTTL time.Duration
	limit   int
	now     func() time.Time

	mu    sync.Mutex
	items map[string]guideEntry
}

type guideEntry struct {
	text    string
	expires time.Time
}

// Пределы кеша: языков и уровней на платформе единицы, и 32 ключа хватает с
// запасом, а мусорный уровень из чужого урока не раздует кеш.
const (
	levelGuideTTL     = 30 * time.Minute
	levelGuideFailTTL = time.Minute
	levelGuideLimit   = 32
)

func newLevelGuide(runner *lessonpipe.Runner) *levelGuide {
	return &levelGuide{
		fetch: func(ctx context.Context, language, level string) (string, error) {
			out, err := runner.LevelGuidance(ctx, language, level)
			return out.Text, err
		},
		ttl: levelGuideTTL, failTTL: levelGuideFailTTL, limit: levelGuideLimit, now: time.Now,
		items: map[string]guideEntry{},
	}
}

// LevelGuidance — указания уровня; "" — их нет или справочник недоступен.
func (g *levelGuide) LevelGuidance(ctx context.Context, language, level string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	level = strings.ToLower(strings.TrimSpace(level))
	if language == "" || level == "" {
		return ""
	}
	key := language + "|" + level
	now := g.now()

	g.mu.Lock()
	if e, ok := g.items[key]; ok && now.Before(e.expires) {
		g.mu.Unlock()
		return e.text
	}
	g.mu.Unlock()

	// Запрос — без блокировки: справочник может отвечать секунды, и ждать
	// его не должны сессии с другими уровнями.
	text, err := g.fetch(ctx, language, level)
	ttl := g.ttl
	if err != nil {
		logx.From(ctx).Warn("указания уровня недоступны — голосовой разговор без них",
			"language", language, "level", level, "err", err)
		text, ttl = "", g.failTTL
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.evict(now)
	g.items[key] = guideEntry{text: text, expires: now.Add(ttl)}
	return text
}

// evict освобождает место под новый ключ: сначала просроченные, затем те,
// что истекают раньше всех.
func (g *levelGuide) evict(now time.Time) {
	if len(g.items) < g.limit {
		return
	}
	for k, e := range g.items {
		if !now.Before(e.expires) {
			delete(g.items, k)
		}
	}
	for len(g.items) >= g.limit {
		oldest, at := "", time.Time{}
		for k, e := range g.items {
			if oldest == "" || e.expires.Before(at) {
				oldest, at = k, e.expires
			}
		}
		delete(g.items, oldest)
	}
}
