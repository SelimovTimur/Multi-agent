package content

import (
	"container/list"
	"sync"
	"time"

	"github.com/google/uuid"
)

// versionCache — небольшой кеш производных данных версии урока в памяти
// процесса: заданий урока и разбора упражнений для учителя.
//
// Зачем он вообще: экран «Урок в классе» опрашивает сервер раз в 5 секунд у
// учителя и у каждого ученика, и каждый опрос спрашивает, из каких заданий
// состоит урок. Ответ у опубликованной версии не меняется никогда, и читать
// его из базы тридцать раз в пять секунд на класс незачем.
//
// Чего он не делает: не хранит ничего, без чего реплика API не может
// ответить (инвариант И-1). Промах — это просто запрос в базу; убитый
// процесс теряет кеш, и никто этого не замечает. Размер ограничен сверху,
// старые записи вытесняются (И-5).
type versionCache[V any] struct {
	mu    sync.Mutex
	max   int
	items map[uuid.UUID]*list.Element
	order *list.List // спереди — самые свежие обращения
	now   func() time.Time
}

type versionCacheEntry[V any] struct {
	versionID uuid.UUID
	// orgID — тенант, для которого запись прочитана. Идентификаторы версий
	// уникальны и так, но запрос чужого тенанта не должен находить запись даже
	// случайно: промах дешевле, чем проверка, которую легко забыть.
	orgID   uuid.UUID
	value   V
	expires time.Time
}

func newVersionCache[V any](max int) *versionCache[V] {
	return &versionCache[V]{
		max:   max,
		items: make(map[uuid.UUID]*list.Element, max),
		order: list.New(),
		now:   time.Now,
	}
}

// get отдаёт значение, если оно есть, не устарело и прочитано для того же
// тенанта. Значение общее для всех читателей — менять его нельзя.
func (c *versionCache[V]) get(orgID, versionID uuid.UUID) (V, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[versionID]
	if !ok {
		return zero, false
	}
	e := el.Value.(*versionCacheEntry[V])
	if e.orgID != orgID || !c.now().Before(e.expires) {
		c.order.Remove(el)
		delete(c.items, versionID)
		return zero, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

// put кладёт значение на ttl. Переполненный кеш теряет самую давнюю запись.
func (c *versionCache[V]) put(orgID, versionID uuid.UUID, value V, ttl time.Duration) {
	if c.max <= 0 || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	expires := c.now().Add(ttl)
	if el, ok := c.items[versionID]; ok {
		e := el.Value.(*versionCacheEntry[V])
		e.orgID, e.value, e.expires = orgID, value, expires
		c.order.MoveToFront(el)
		return
	}
	c.items[versionID] = c.order.PushFront(&versionCacheEntry[V]{
		versionID: versionID, orgID: orgID, value: value, expires: expires,
	})
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*versionCacheEntry[V]).versionID)
	}
}

// len — сколько записей сейчас в кеше. Для тестов предела.
func (c *versionCache[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// publishedVersionTTL — сколько живёт запись опубликованной версии.
//
// Кешируются только опубликованные версии: их содержимое неизменяемо (это
// держит триггер в базе), и кеш ничего не решает за базу. Готовую версию
// учитель ещё может править — её задания читаются из базы каждый раз, иначе
// правка доходила бы до класса с опозданием. Назначают классам на практике
// опубликованные: других окно назначения не предлагает.
//
// Срок — не ради свежести, а чтобы кеш не держал память под уроками, которые
// давно никто не открывает. Две минуты против опроса раз в 5 секунд — это
// один запрос в базу вместо двадцати четырёх на каждую реплику.
const publishedVersionTTL = 2 * time.Minute

// versionTTL — срок записи версии с этим статусом; ноль — не кешировать.
func versionTTL(status string) time.Duration {
	if status == VersionPublished {
		return publishedVersionTTL
	}
	return 0
}
