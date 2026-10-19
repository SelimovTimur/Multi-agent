package learning

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Classes — что модулю нужно знать о классах.
//
// Интерфейс, а не запрос в схему classroom: назначение живёт в learning,
// класс — в classroom, и джойн между ними закрыл бы дорогу к разделению
// на сервисы.
type Classes interface {
	// OwnedClass возвращает класс учителя. Чужой или несуществующий — ошибка 404.
	OwnedClass(ctx context.Context, actor iam.Actor, classID uuid.UUID) (ClassInfo, error)
	// StudentClasses — активные классы ученика.
	StudentClasses(ctx context.Context, actor iam.Actor) ([]ClassInfo, error)
	// Roster — состав класса.
	//
	// Порт пришлось расширить: аналитика считает долю прошедших от числа
	// учеников класса, а по ClassInfo размер класса не восстановить. Считать
	// же учеников по попыткам нельзя — именно те, кто ни одного урока не
	// открыл, и есть главная новость этого экрана.
	Roster(ctx context.Context, actor iam.Actor, classID uuid.UUID) ([]ClassMember, error)
	// ClassesInfo — классы пачкой по идентификаторам, в пределах тенанта.
	//
	// Зеркало Lessons.VersionsInfo и по той же причине: список «в какие классы
	// выдан урок» рисуется на каждой строке библиотеки, и запрос на класс
	// превратил бы её в N+1.
	//
	// Без актора намеренно: имена классов внутри одной организации не секрет —
	// назначить урок в класс мог только тот, у кого есть доступ и к уроку, и к
	// классу. Тенант при этом проверяется всегда: org_id обязателен.
	ClassesInfo(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]ClassInfo, error)
}

// ClassMember — ученик класса: ровно столько, сколько нужно аналитике.
//
// Персональных данных здесь нет и быть не может: у ученика по 152-ФЗ есть
// только отображаемое имя и короткий тег для различения тёзок.
type ClassMember struct {
	ID          uuid.UUID
	DisplayName string
	PublicTag   string
}

// ClassInfo — минимум о классе, нужный назначениям.
type ClassInfo struct {
	ID      uuid.UUID
	Title   string
	Subject string
	Level   string
	Lang    string
}

// Lessons — что модулю нужно знать о версиях уроков.
type Lessons interface {
	// OwnedVersion проверяет, что версия принадлежит уроку этого учителя.
	OwnedVersion(ctx context.Context, actor iam.Actor, versionID uuid.UUID) (VersionInfo, error)
	// VersionsInfo — сведения о версиях пачкой, для списков.
	VersionsInfo(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]VersionInfo, error)
	// StudentContent — содержимое версии БЕЗ ключа ответов.
	//
	// Отдельный метод, а не флаг у существующего: флаг легко передать
	// неверно, а отдельный метод нельзя вызвать «случайно с ключами».
	StudentContent(ctx context.Context, orgID, versionID uuid.UUID) (any, error)
	// CheckAnswer сверяет ответ ученика с ключом упражнения.
	//
	// Проверяет content, а не learning: ключ хранится там, и его формат —
	// его дело. Сюда возвращается только «верно/неверно», правильный ответ
	// и цена ответа.
	CheckAnswer(ctx context.Context, orgID, versionID, exerciseID uuid.UUID,
		itemID, value string) (AnswerCheck, error)
	// QuestionCount — сколько в уроке вопросов, на которые ученик отвечает.
	//
	// Нужен для честной доли верных: считать её от числа данных ответов
	// значит показывать тем больше, чем меньше ученик сделал.
	QuestionCount(ctx context.Context, orgID, versionID uuid.UUID) (int, error)
	// LessonTasks — задания версий урока в порядке прохождения, пачкой.
	//
	// Из чего урок состоит и как называются его шаги, знает content: здесь
	// хранятся только ключи выданного. Пачкой — потому что «выдано N из 8»
	// стоит в каждой строке вкладки «Уроки» и «Моих занятий».
	LessonTasks(ctx context.Context, orgID uuid.UUID, versionIDs []uuid.UUID) (map[uuid.UUID][]LessonTask, error)
	// ExerciseReviews — вопросы упражнений версии с текстами вариантов и
	// верными ответами, по упражнению.
	//
	// Только для экранов учителя: это ключ ответов, переведённый в слова.
	// Ручки ученика этот метод не вызывают — как и с StudentContent,
	// отдельный метод нельзя позвать «случайно с ключами».
	ExerciseReviews(ctx context.Context, orgID, versionID uuid.UUID) (map[uuid.UUID]ExerciseReview, error)
	// LessonWords — лексика версии урока: выражения с переводом, в порядке
	// урока. По ней аналитика ищет трудные слова класса.
	LessonWords(ctx context.Context, orgID, versionID uuid.UUID) ([]LessonWord, error)
}

// AnswerCheck — итог проверки ответа. Зеркало content.AnswerCheck.
type AnswerCheck struct {
	Correct  bool
	Expected string
	// Kind — вид задания (сопоставление, пропуски, продолжение). Ложится в
	// событие ответа: аналитика по видам осмысленна, по номерам шагов — нет,
	// номер у разных уроков означает разное.
	Kind  string
	Score int
}

// VersionInfo — минимум о версии урока.
type VersionInfo struct {
	ID       uuid.UUID
	LessonID uuid.UUID
	Version  int
	// Title — название версии: служебная пометка учителя при публикации
	// («после замечаний методиста»). Ученику не показывается.
	Title string
	// LessonTitle — название урока. Это то, что видят класс и ученик:
	// заданию с именем «исправила опечатки» ребёнок не обрадуется.
	LessonTitle string
	Level       string
	Status      string
}

// Name — как назвать урок человеку. Название версии подменять им нельзя.
func (v VersionInfo) Name() string {
	if v.LessonTitle != "" {
		return v.LessonTitle
	}
	return v.Title
}

// Service — прикладная логика назначений и прохождения.
type Service struct {
	pool    *pg.Pool
	store   *Store
	classes Classes
	lessons Lessons

	// Материал разговора подключается отдельно (WithMaterial): без него
	// модуль работает, просто разговор недоступен.
	material LessonMaterial
	// Голосовой разговор без кнопки записи (WithLiveVoice).
	live LiveVoice
	// Разговор по целям урока (WithConversation) и указания уровня для
	// голосового разговора (WithLevelGuide).
	conversation Conversation
	guide        LevelGuide

	// reportSlots ограничивает фоновые разборы разговоров одного процесса.
	// Разбор идёт в пуле чата, где ученики ждут реплик аватара: отчёт по
	// классу из тридцати закрытых разговоров не должен разом занять все
	// слоты. Это ограничитель, а не состояние: работа и аренда живут в базе.
	reportSlots chan struct{}
	// reports — запущенные разборы, чтобы остановка процесса могла их
	// дождаться. Не дождалась — не беда: аренда в базе истечёт, и разбор
	// перезапустится.
	reports sync.WaitGroup

	// zone — часовой пояс школы. «До конца дня» и «срок 03.10» — это сутки
	// школы, а не сервера: контейнер живёт в UTC, и выдача «до конца дня»
	// закрывалась бы в три часа ночи по Москве.
	zone *time.Location
}

// DefaultSchoolZone — часовой пояс школы по умолчанию.
const DefaultSchoolZone = "Europe/Moscow"

func NewService(pool *pg.Pool, classes Classes, lessons Lessons) *Service {
	return &Service{pool: pool, store: NewStore(), classes: classes, lessons: lessons,
		zone: defaultZone(), reportSlots: make(chan struct{}, reportConcurrency)}
}

// WithSchoolZone задаёт часовой пояс школы (SCHOOL_TZ).
func (s *Service) WithSchoolZone(loc *time.Location) *Service {
	if loc != nil {
		s.zone = loc
	}
	return s
}

// Zone — часовой пояс школы: в нём отдаются сроки и время выдач.
func (s *Service) Zone() *time.Location { return s.zone }

// defaultZone — Москва; без базы часовых поясов в системе — фиксированные
// +03:00: у Москвы нет перехода на летнее время.
func defaultZone() *time.Location {
	if loc, err := time.LoadLocation(DefaultSchoolZone); err == nil {
		return loc
	}
	return time.FixedZone("MSK", 3*60*60)
}

// ---------------------------------------------------------------------------
// Назначение урока
// ---------------------------------------------------------------------------

// AssignInput — что задаёт учитель при назначении урока.
//
// Сроков у назначения больше нет: урок назначается закрытым, а открывают его
// выдачи заданий — на уроке или на дом со своим сроком.
type AssignInput struct {
	LessonVersionID uuid.UUID
	ClassID         uuid.UUID
}

// errAlreadyAssigned — урок уже назначен классу.
var errAlreadyAssigned = errors.New("learning: урок уже назначен классу")

// Assign прикрепляет версию урока к классу. Задания при этом закрыты:
// ученик урока не видит, пока учитель не выдаст задания.
//
// Назначается именно версия: правка урока после выдачи не должна менять то,
// что видит класс.
func (s *Service) Assign(ctx context.Context, actor iam.Actor, in AssignInput) (Assignment, error) {
	if _, err := s.classes.OwnedClass(ctx, actor, in.ClassID); err != nil {
		return Assignment{}, err
	}
	version, err := s.lessons.OwnedVersion(ctx, actor, in.LessonVersionID)
	if err != nil {
		return Assignment{}, err
	}
	// Готовая версия ещё редактируется. Только опубликованная сохраняет
	// выданные ключи заданий и ответы учеников неизменными.
	if version.Status != "published" {
		return Assignment{}, httpx.Conflict(
			"Назначить можно только опубликованную версию. Сначала опубликуйте урок")
	}

	a := Assignment{
		ID:              idgen.New(),
		OrgID:           actor.OrgID,
		LessonVersionID: in.LessonVersionID,
		ClassID:         in.ClassID,
		TargetType:      TargetClass,
		State:           StateActive,
		CreatedBy:       actor.UserID,
	}
	var out Assignment
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Урок назначается классу один раз — любой версией. Иначе в классе
		// два «Гаилә» с разными выдачами, и ученик не поймёт, какой делать.
		// Проверка и вставка — под блокировкой класса: двойной клик с двумя
		// версиями иначе прошёл бы мимо проверки дважды.
		if err := s.store.LockClassAssignments(ctx, tx, in.ClassID); err != nil {
			return err
		}
		versionIDs, err := s.store.OpenAssignmentVersions(ctx, tx, actor.OrgID, in.ClassID)
		if err != nil {
			return err
		}
		if len(versionIDs) > 0 {
			infos, err := s.lessons.VersionsInfo(ctx, actor.OrgID, versionIDs)
			if err != nil {
				return err
			}
			for _, id := range versionIDs {
				if id == in.LessonVersionID || infos[id].LessonID == version.LessonID {
					return errAlreadyAssigned
				}
			}
		}
		out, err = s.store.CreateAssignment(ctx, tx, a)
		return err
	})
	if errors.Is(err, errAlreadyAssigned) || pg.IsUniqueViolation(err) {
		return Assignment{}, httpx.Conflict("Этот урок уже назначен классу")
	}
	if err != nil {
		return Assignment{}, wrapErr(err, "Не удалось назначить урок")
	}
	telemetry.Emit(ctx, telemetry.Event{Name: "lesson_assigned", EntityID: out.ID, AssignmentID: out.ID, ClassID: out.ClassID, LessonID: version.LessonID})
	return out, nil
}

// HasVersionAssignments нужен библиотеке для защиты ранее выданных ready
// версий. Сведения берутся через порт, без обращения к чужой схеме.
func (s *Service) HasVersionAssignments(ctx context.Context, orgID, versionID uuid.UUID) (bool, error) {
	return s.store.HasVersionAssignments(ctx, s.pool, orgID, versionID)
}

// wrapErr пропускает ошибки с ответом для клиента как есть, остальные
// заворачивает в 500.
func wrapErr(err error, msg string) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return err
	}
	return httpx.Internal(msg).Wrap(err)
}

// validateWindow проверяет окно доступа.
func validateWindow(opensAt, closesAt *time.Time, now time.Time) error {
	fields := map[string]string{}
	if opensAt != nil && closesAt != nil && !closesAt.After(*opensAt) {
		fields["closes_at"] = "Закрытие должно быть позже открытия"
	}
	// Закрытие в прошлом означает урок, который никто не успеет пройти.
	// Открытие в прошлом — норма: «выдать прямо сейчас».
	if closesAt != nil && closesAt.Before(now) {
		fields["closes_at"] = "Срок закрытия уже прошёл"
	}
	if len(fields) > 0 {
		return httpx.Validation("Проверьте сроки доступа", fields)
	}
	return nil
}

// initialState: без времени открытия урок доступен сразу.
func initialState(opensAt *time.Time, now time.Time) string {
	if opensAt != nil && opensAt.After(now) {
		return StateScheduled
	}
	return StateActive
}

// Assignment отдаёт назначение учителю с проверкой владения классом.
func (s *Service) Assignment(ctx context.Context, actor iam.Actor, id uuid.UUID) (Assignment, error) {
	a, err := s.store.AssignmentByID(ctx, s.pool, actor.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		return Assignment{}, httpx.NotFound("Назначение не найдено")
	}
	if err != nil {
		return Assignment{}, httpx.Internal("Не удалось прочитать назначение").Wrap(err)
	}
	if _, err := s.classes.OwnedClass(ctx, actor, a.ClassID); err != nil {
		return Assignment{}, httpx.NotFound("Назначение не найдено")
	}
	return a, nil
}

// AssignmentView — назначение вместе со сведениями об уроке.
type AssignmentView struct {
	Assignment Assignment
	Version    VersionInfo
	Class      ClassInfo
	// Summary — «выдано N из 8», «идёт урок», «на дом до …».
	Summary AssignmentSummary
}

// View дополняет назначение сведениями об уроке и классе.
//
// Нужен ответам на создание и правку: без него фронт получил бы голый
// идентификатор версии и был вынужден идти за названием вторым запросом.
func (s *Service) View(ctx context.Context, actor iam.Actor, a Assignment) AssignmentView {
	out := AssignmentView{Assignment: a}
	if versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, []uuid.UUID{a.LessonVersionID}); err == nil {
		out.Version = versions[a.LessonVersionID]
	}
	if class, err := s.classes.OwnedClass(ctx, actor, a.ClassID); err == nil {
		out.Class = class
	}
	if sums, err := s.summaries(ctx, actor.OrgID, []Assignment{a}); err == nil {
		out.Summary = sums[a.ID]
	}
	return out
}

// ClassAssignments — вкладка «Уроки» класса.
func (s *Service) ClassAssignments(ctx context.Context, actor iam.Actor, classID uuid.UUID,
	state string) ([]AssignmentView, error) {
	class, err := s.classes.OwnedClass(ctx, actor, classID)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListByClass(ctx, s.pool, actor.OrgID, classID, state)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать назначения").Wrap(err)
	}
	return s.withVersions(ctx, actor.OrgID, items, map[uuid.UUID]ClassInfo{classID: class})
}

// withVersions дочитывает сведения о версиях одним запросом.
//
// Пакетно, а не по одному на строку: на вкладке класса бывает два десятка
// уроков, и N+1 здесь виден невооружённым глазом.
func (s *Service) withVersions(ctx context.Context, orgID uuid.UUID, items []Assignment,
	classes map[uuid.UUID]ClassInfo) ([]AssignmentView, error) {
	if len(items) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, a := range items {
		ids = append(ids, a.LessonVersionID)
	}
	versions, err := s.lessons.VersionsInfo(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	sums, err := s.summaries(ctx, orgID, items)
	if err != nil {
		return nil, err
	}

	out := make([]AssignmentView, 0, len(items))
	for _, a := range items {
		out = append(out, AssignmentView{
			Assignment: a,
			Version:    versions[a.LessonVersionID],
			Class:      classes[a.ClassID],
			Summary:    sums[a.ID],
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Продление, закрытие, отмена
// ---------------------------------------------------------------------------

// Reschedule сдвигает сроки. Действует на весь класс сразу.
//
// Индивидуального продления одному ученику нет намеренно: это ломает
// сравнимость результатов внутри класса и запутывает самого учителя.
func (s *Service) Reschedule(ctx context.Context, actor iam.Actor, id uuid.UUID,
	opensAt, closesAt *time.Time) (Assignment, error) {
	a, err := s.Assignment(ctx, actor, id)
	if err != nil {
		return Assignment{}, err
	}

	now := time.Now()
	if err := validateWindow(opensAt, closesAt, now); err != nil {
		return Assignment{}, err
	}
	overlap, err := s.store.OverlappingExists(ctx, s.pool, actor.OrgID, a.ClassID,
		a.LessonVersionID, opensAt, closesAt, a.ID)
	if err != nil {
		return Assignment{}, httpx.Internal("Не удалось изменить сроки").Wrap(err)
	}
	if overlap {
		return Assignment{}, httpx.Conflict("Новый период пересекается с другим назначением этого урока")
	}

	state := initialState(opensAt, now)
	if closesAt != nil && !closesAt.After(now) {
		state = StateClosed
	}
	if err := s.store.UpdateWindow(ctx, s.pool, actor.OrgID, id, opensAt, closesAt, state); err != nil {
		return Assignment{}, httpx.Internal("Не удалось изменить сроки").Wrap(err)
	}
	a.OpensAt, a.ClosesAt, a.State = opensAt, closesAt, state
	return a, nil
}

// Close закрывает доступ досрочно. Ответы учеников сохраняются.
func (s *Service) Close(ctx context.Context, actor iam.Actor, id uuid.UUID) (Assignment, error) {
	a, err := s.Assignment(ctx, actor, id)
	if err != nil {
		return Assignment{}, err
	}
	if err := s.closeAssignment(ctx, actor.OrgID, id); err != nil {
		return Assignment{}, httpx.Internal("Не удалось закрыть доступ").Wrap(err)
	}
	// Перечитываем, а не правим состояние на месте: момент закрытия ставит
	// база, и в ответе должно стоять то же время, что и в ней, — иначе
	// страница класса скажет «закрыт досрочно» до первого обновления.
	a, err = s.Assignment(ctx, actor, id)
	if err != nil {
		return Assignment{}, err
	}
	return a, nil
}

// Unassign снимает назначение.
//
// Если урок никто не открывал — удаляем запись целиком. Если открывали —
// только закрываем: за попытками стоят ответы учеников, и удалять их
// молча по нажатию «снять» неприемлемо.
func (s *Service) Unassign(ctx context.Context, actor iam.Actor, id uuid.UUID) (string, error) {
	if _, err := s.Assignment(ctx, actor, id); err != nil {
		return "", err
	}
	n, err := s.store.CountAttempts(ctx, s.pool, actor.OrgID, id)
	if err != nil {
		return "", httpx.Internal("Не удалось снять назначение").Wrap(err)
	}
	if n > 0 {
		if err := s.closeAssignment(ctx, actor.OrgID, id); err != nil {
			return "", httpx.Internal("Не удалось снять назначение").Wrap(err)
		}
		return "closed", nil
	}
	if err := s.store.DeleteAssignment(ctx, s.pool, actor.OrgID, id); err != nil {
		return "", httpx.Internal("Не удалось снять назначение").Wrap(err)
	}
	return "deleted", nil
}

// closeAssignment снимает урок с класса: назначение закрыто, открытые
// выдачи закрываются тем же моментом. Одной транзакцией — иначе снятый урок
// с открытой домашкой ученик видел бы до первого обновления.
func (s *Service) closeAssignment(ctx context.Context, orgID, id uuid.UUID) error {
	return pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.store.SetState(ctx, tx, orgID, id, StateClosed); err != nil {
			return err
		}
		return s.store.CloseOpenIssues(ctx, tx, orgID, id, time.Now())
	})
}

// SyncStates переводит назначения по времени. Вызывается фоновой задачей.
//
// Теперь только открывает: запланированное назначение становится активным,
// когда наступил его opens_at. Закрывать по времени больше нечего — сроки
// живут у выдач и проверяются при чтении, а закрытое назначение означает
// «урок снят с класса»: снимать урок по сроку домашки нельзя, её продлят.
func (s *Service) SyncStates(ctx context.Context) error {
	n, err := s.store.SyncStates(ctx, s.pool, time.Now())
	if err != nil {
		return err
	}
	if n > 0 {
		logx.From(ctx).Info("состояния назначений обновлены", "changed", n)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Карточка ученика
// ---------------------------------------------------------------------------

// StudentCard — что учитель видит про одного ученика класса.
//
// Строки — все уроки, назначенные классу, а не только начатые: «не открывал»
// для учителя такая же новость, как «ошибся». Доля верных и «пройден»
// считаются по правилам вкладки «Аналитика» — от вопросов урока и по отметке
// completed. Раньше карточка делила на данные ответы и засчитывала
// пролистанные уроки, и у одного ученика на соседних экранах было два разных
// процента.
type StudentCard struct {
	Summary CardSummary
	Lessons []StudentLessonResult
}

// CardSummary — строка сводки над списком уроков.
type CardSummary struct {
	Assigned int
	Passed   int
	// Correct и Questions — верные ответы и вопросы уроков, которые ученик
	// открывал. Знаменатель — вопросы, а не ответы: ответивший на один вопрос
	// из пяти иначе получил бы сто процентов.
	Correct    int
	Questions  int
	LastSeenAt *time.Time
}

// StudentLessonResult — урок класса в карточке ученика.
type StudentLessonResult struct {
	Assignment Assignment
	Version    VersionInfo
	// Attempt — nil, пока ученик урок не открывал.
	Attempt *Attempt
	// TasksTotal и TasksDone — «3 из 8 заданий».
	TasksTotal int
	TasksDone  int
	// Issues — выдачи урока по времени: что было на уроке, а что дома.
	Issues []CardIssue
	// Conversation — итог разговора с аватаром; nil — разговора не было.
	Conversation *CardConversation
}

// CardConversation — итог разговора в строке урока: «3 из 4 целей».
type CardConversation struct {
	// Ready — разбор готов. Пока он не готов, целей не знаем и показываем
	// только, что разговор был.
	Ready    bool
	Credited int
	Goals    int
}

// CardIssue — выдача в строке истории.
type CardIssue struct {
	Issue Issue
	Open  bool
	Tasks []CardTask
}

// CardTask — задание выдачи у ученика: сделано, пока не сделано или не
// сделано.
type CardTask struct {
	Key   string
	N     int
	State string
	// Correct и Total — у заданий, которые засчитываются ответами («5/5»);
	// nil у остальных — у них «✓» без дроби.
	Correct *int
	Total   *int
}

// StudentCardFor собирает карточку ученика класса.
//
// Доступ проверяется через класс: учитель видит только своих учеников.
// Право students.own объявлено на маршруте, но принадлежность класса всё
// равно проверяется здесь — маршрут не знает, чей это ученик. Ученик чужого
// класса — 404: ни в составе, ни в попытках этого класса его нет. Исключённый
// ученик в составе не числится, но его работа в классе осталась, и карточка
// открывается по ней.
func (s *Service) StudentCardFor(ctx context.Context, actor iam.Actor, classID, studentID uuid.UUID) (StudentCard, error) {
	if _, err := s.classes.OwnedClass(ctx, actor, classID); err != nil {
		return StudentCard{}, err
	}
	attempts, err := s.store.AttemptsInClass(ctx, s.pool, actor.OrgID, classID, studentID)
	if err != nil {
		return StudentCard{}, httpx.Internal("Не удалось прочитать историю").Wrap(err)
	}
	if len(attempts) == 0 {
		members, err := s.classes.Roster(ctx, actor, classID)
		if err != nil {
			return StudentCard{}, err
		}
		if !slices.ContainsFunc(members, func(m ClassMember) bool { return m.ID == studentID }) {
			return StudentCard{}, httpx.NotFound("Ученик не найден")
		}
	}

	assignments, err := s.store.ListByClass(ctx, s.pool, actor.OrgID, classID, "")
	if err != nil {
		return StudentCard{}, httpx.Internal("Не удалось прочитать назначения").Wrap(err)
	}
	if len(assignments) == 0 {
		return StudentCard{Lessons: []StudentLessonResult{}}, nil
	}

	// Всё пачкой: у класса за год десятки уроков, и версия, выдачи или
	// задания урока запросом на строку превратили бы карточку в N+1.
	assignmentIDs := make([]uuid.UUID, 0, len(assignments))
	versionIDs := make([]uuid.UUID, 0, len(assignments))
	for _, a := range assignments {
		assignmentIDs = append(assignmentIDs, a.ID)
		versionIDs = append(versionIDs, a.LessonVersionID)
	}
	attemptIDs := make([]uuid.UUID, 0, len(attempts))
	for _, at := range attempts {
		attemptIDs = append(attemptIDs, at.ID)
	}
	versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, versionIDs)
	if err != nil {
		return StudentCard{}, err
	}
	tasks, err := s.lessons.LessonTasks(ctx, actor.OrgID, versionIDs)
	if err != nil {
		return StudentCard{}, err
	}
	issues, err := s.store.IssuesOfAssignments(ctx, s.pool, actor.OrgID, assignmentIDs)
	if err != nil {
		return StudentCard{}, httpx.Internal("Не удалось прочитать историю").Wrap(err)
	}
	progress, err := s.store.ProgressOfAttempts(ctx, s.pool, actor.OrgID, attemptIDs)
	if err != nil {
		return StudentCard{}, httpx.Internal("Не удалось прочитать историю").Wrap(err)
	}
	convs, err := s.store.ConversationsOfAttempts(ctx, s.pool, actor.OrgID, attemptIDs)
	if err != nil {
		return StudentCard{}, httpx.Internal("Не удалось прочитать разговоры").Wrap(err)
	}
	return assembleCard(assignments, attempts, versions, tasks, issues, progress, convs, time.Now()), nil
}

// assembleCard — арифметика карточки без базы: строки уроков, сводка и итог
// разговора. Порядок строк — как у назначений, от нового урока к старому.
func assembleCard(assignments []Assignment, attempts []Attempt, versions map[uuid.UUID]VersionInfo,
	tasks map[uuid.UUID][]LessonTask, issues map[uuid.UUID][]Issue, progress []TaskProgress,
	convs []ConversationRow, now time.Time) StudentCard {
	attemptOf := make(map[uuid.UUID]Attempt, len(attempts))
	for _, at := range attempts {
		attemptOf[at.AssignmentID] = at
	}
	progressOf := make(map[uuid.UUID][]TaskProgress, len(attempts))
	for _, p := range progress {
		progressOf[p.AttemptID] = append(progressOf[p.AttemptID], p)
	}
	convOf := make(map[uuid.UUID]ConversationRow, len(convs))
	for _, c := range convs {
		convOf[c.AttemptID] = c
	}

	out := StudentCard{Summary: CardSummary{Assigned: len(assignments)}, Lessons: make([]StudentLessonResult, 0, len(assignments))}
	for _, a := range assignments {
		at, started := attemptOf[a.ID]
		row := cardRow(at, a, tasks[a.LessonVersionID], issues[a.ID], progressOf[at.ID], now)
		row.Version = versions[a.LessonVersionID]
		if !started {
			// Строка без попытки: выдачи и «не сделано» видны, а счёта нет.
			row.Attempt = nil
			out.Lessons = append(out.Lessons, row)
			continue
		}
		if c, ok := convOf[at.ID]; ok {
			row.Conversation = cardConversation(c)
		}
		if at.Completed {
			out.Summary.Passed++
		}
		out.Summary.Correct += at.CorrectAnswers
		out.Summary.Questions += at.TotalQuestions
		if seen := at.LastSeenAt; out.Summary.LastSeenAt == nil || seen.After(*out.Summary.LastSeenAt) {
			out.Summary.LastSeenAt = &seen
		}
		out.Lessons = append(out.Lessons, row)
	}
	return out
}

// cardConversation — итог разговора для строки урока. Разговора не было —
// nil: ни одной реплики ученика и движок не запускался.
func cardConversation(c ConversationRow) *CardConversation {
	if c.Turns == 0 && !c.HasEngine {
		return nil
	}
	out := &CardConversation{}
	if _, analysis, ok := readyReport(c); ok {
		out.Ready = true
		out.Goals = len(analysis.Goals)
		for _, g := range analysis.Goals {
			if g.Credited {
				out.Credited++
			}
		}
	}
	return out
}

// cardRow — арифметика строки истории без базы.
func cardRow(at Attempt, a Assignment, tasks []LessonTask, issues []Issue, progress []TaskProgress,
	now time.Time) StudentLessonResult {
	b := newBoard(tasks, issues, now, a.State == StateClosed)
	w := newWork(&at, progress)
	out := StudentLessonResult{Attempt: &at, Assignment: a, TasksTotal: len(tasks), Issues: []CardIssue{}}
	for _, t := range tasks {
		if w.status(t).Done {
			out.TasksDone++
		}
	}
	for i, is := range b.issues {
		ci := CardIssue{Issue: is, Open: b.open[i], Tasks: []CardTask{}}
		for _, k := range b.issueKeys(i) {
			t, _ := b.task(k)
			st := w.status(t)
			ct := CardTask{Key: k, N: t.N, State: cardState(st, b.closed[i])}
			if t.Questions > 0 {
				correct, total := st.Correct, t.Questions
				ct.Correct, ct.Total = &correct, &total
			}
			ci.Tasks = append(ci.Tasks, ct)
		}
		out.Issues = append(out.Issues, ci)
	}
	return out
}
