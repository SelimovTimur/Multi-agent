package learning

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

// inactiveWindow — сколько ученик может не работать с уроками, прежде чем
// сводка назовёт его по имени.
//
// Неделя, а не месяц: аналитику открывают, чтобы увидеть, кто выпал из работы
// сейчас. За месяц не пропадает почти никто, и строка перестанет что-либо
// значить.
const inactiveWindow = 7 * 24 * time.Hour

// ClassAnalytics — вкладка «Аналитика» класса: сводка и таблица результатов
// «ученики × уроки».
//
// Один ответ на весь экран, без запроса на клетку: средние по строке и
// столбцу считаются из тех же клеток, и собирай их фронт по частям — между
// запросами ученик успеет ответить, и среднее разойдётся с клетками.
//
// Клетка считается по правилам карточки ученика: доля верных — от вопросов
// урока в попытке, итог разговора — по готовому разбору. Числа в клетке и в
// строке урока карточки обязаны совпадать.
type ClassAnalytics struct {
	// Assigned — уроки, назначенные классу; Expected — ученики × уроки.
	Assigned int
	Expected int
	// Passed — сколько клеток таблицы пройдено до конца. Считаются только
	// ученики из состава: сводка описывает ту же таблицу, что под ней.
	Passed int
	// Inactive — ученики, которые неделю не работали с уроками класса.
	Inactive []ClassMember

	// Lessons — столбцы, от нового урока к старому.
	Lessons []ResultLesson
	// Students — строки в порядке состава класса. Cells у каждой строки идут
	// в порядке Lessons.
	Students []ResultStudent
}

// ResultLesson — столбец таблицы: урок класса и средние по нему.
type ResultLesson struct {
	AssignmentID uuid.UUID
	LessonID     uuid.UUID
	Title        string
	Level        string
	AssignedAt   time.Time
	Tasks        Share
	Conversation Share
}

// ResultStudent — строка таблицы: ученик, его клетки и средние.
type ResultStudent struct {
	Member       ClassMember
	LastSeenAt   *time.Time
	Cells        []ResultCell
	Tasks        Share
	Conversation Share
}

// ResultCell — что ученик сделал в одном уроке.
type ResultCell struct {
	// Started — ученик урок открывал. Не открывал — клетка «не пройден».
	Started   bool
	Completed bool
	Correct   int
	Questions int
	// Conversation — итог разговора; nil — разговора не было.
	Conversation *CardConversation
}

// Share — доля «часть из целого» для средних. Среднее — не среднее
// процентов клеток, а сумма верных на сумму вопросов: так строка ученика
// совпадает со сводкой его карточки, а урок с тремя вопросами не весит
// столько же, сколько урок с тридцатью.
type Share struct {
	Part  int
	Total int
}

// Percent — доля в процентах; nil, когда считать не из чего.
func (s Share) Percent() *int { return percentOrNil(s.Part, s.Total) }

// ClassAnalyticsFor собирает аналитику класса.
//
// Владение классом проверяется здесь, а не маршрутом: право students.own
// означает «учитель видит своих учеников», но чьи это ученики, знает только
// classroom. Ровно так же устроена карточка ученика.
func (s *Service) ClassAnalyticsFor(ctx context.Context, actor iam.Actor,
	classID uuid.UUID) (ClassAnalytics, error) {

	if _, err := s.classes.OwnedClass(ctx, actor, classID); err != nil {
		return ClassAnalytics{}, err
	}

	// Строки таблицы — состав класса из classroom, а не журнал ответов:
	// ученик, не открывший ни одного урока, — главное, что учитель должен
	// увидеть, и строка «не пройден» говорит об этом прямо.
	members, err := s.classes.Roster(ctx, actor, classID)
	if err != nil {
		return ClassAnalytics{}, err
	}
	assignments, err := s.store.ListByClass(ctx, s.pool, actor.OrgID, classID, "")
	if err != nil {
		return ClassAnalytics{}, httpx.Internal("Не удалось прочитать назначения").Wrap(err)
	}
	attempts, err := s.store.ClassAttempts(ctx, s.pool, actor.OrgID, classID)
	if err != nil {
		return ClassAnalytics{}, httpx.Internal("Не удалось посчитать аналитику").Wrap(err)
	}
	convs, err := s.store.ConversationsOfClass(ctx, s.pool, actor.OrgID, classID)
	if err != nil {
		return ClassAnalytics{}, httpx.Internal("Не удалось прочитать разговоры").Wrap(err)
	}

	// Название и уровень урока живут в content. Дочитываем их тем же портом и
	// так же пачкой, что и списки назначений: запрос на столбец превратил бы
	// экран в N+1.
	versionIDs := make([]uuid.UUID, 0, len(assignments))
	for _, a := range assignments {
		versionIDs = append(versionIDs, a.LessonVersionID)
	}
	versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, versionIDs)
	if err != nil {
		return ClassAnalytics{}, err
	}

	return assembleAnalytics(members, assignments, versions, attempts, convs, time.Now()), nil
}

// assembleAnalytics сводит состав, назначения и попытки в таблицу.
//
// Без базы и сети намеренно: клетки, средние и сводка — вся нетривиальная
// арифметика экрана. Её проверяют тесты, а не глаза учителя на проде.
//
// Ушедший из класса ученик строкой не показывается и в сводке не считается:
// составом распоряжается classroom, а его работа остаётся в карточке.
func assembleAnalytics(members []ClassMember, assignments []Assignment, versions map[uuid.UUID]VersionInfo,
	attempts []ClassAttempt, convs []ConversationRow, now time.Time) ClassAnalytics {

	out := ClassAnalytics{
		Assigned: len(assignments),
		Expected: len(assignments) * len(members),
		Inactive: []ClassMember{},
		Lessons:  make([]ResultLesson, 0, len(assignments)),
		Students: make([]ResultStudent, 0, len(members)),
	}

	column := make(map[uuid.UUID]int, len(assignments))
	var firstAssigned time.Time
	for i, a := range assignments {
		column[a.ID] = i
		// Версия могла уехать в архив вместе с уроком — столбец всё равно
		// показываем: назначение было, и ответы по нему есть.
		v := versions[a.LessonVersionID]
		out.Lessons = append(out.Lessons, ResultLesson{
			AssignmentID: a.ID,
			LessonID:     v.LessonID,
			Title:        v.Name(),
			Level:        v.Level,
			AssignedAt:   a.CreatedAt,
		})
		if firstAssigned.IsZero() || a.CreatedAt.Before(firstAssigned) {
			firstAssigned = a.CreatedAt
		}
	}

	convOf := make(map[uuid.UUID]*CardConversation, len(convs))
	for _, c := range convs {
		convOf[c.AttemptID] = cardConversation(c)
	}
	byStudent := make(map[uuid.UUID][]ClassAttempt, len(members))
	for _, at := range attempts {
		byStudent[at.StudentID] = append(byStudent[at.StudentID], at)
	}

	for _, m := range members {
		row := ResultStudent{Member: m, Cells: make([]ResultCell, len(assignments))}
		for _, at := range byStudent[m.ID] {
			i, ok := column[at.AssignmentID]
			if !ok {
				continue
			}
			cell := ResultCell{
				Started:      true,
				Completed:    at.Completed,
				Correct:      at.CorrectAnswers,
				Questions:    at.TotalQuestions,
				Conversation: convOf[at.ID],
			}
			row.Cells[i] = cell
			if seen := at.LastSeenAt; row.LastSeenAt == nil || seen.After(*row.LastSeenAt) {
				row.LastSeenAt = &seen
			}
			if cell.Completed {
				out.Passed++
			}
			addCell(&row.Tasks, &row.Conversation, cell)
			addCell(&out.Lessons[i].Tasks, &out.Lessons[i].Conversation, cell)
		}
		if inactive(row.LastSeenAt, firstAssigned, now) {
			out.Inactive = append(out.Inactive, m)
		}
		out.Students = append(out.Students, row)
	}
	return out
}

// addCell добавляет клетку в средние строки или столбца. Неоткрытый урок и
// неготовый разбор разговора в средние не входят: «не пройден» — это не ноль
// процентов, а отсутствие результата.
func addCell(tasks, conv *Share, c ResultCell) {
	tasks.Part += c.Correct
	tasks.Total += c.Questions
	if c.Conversation != nil && c.Conversation.Ready {
		conv.Part += c.Conversation.Credited
		conv.Total += c.Conversation.Goals
	}
}

// inactive — ученик неделю не работал с уроками класса.
//
// Не открывавший ни одного урока попадает сюда, только если первый урок
// классу выдан больше недели назад: в классе, где урок выдали вчера, «не
// заходил» назвало бы прогульщиками всех.
func inactive(lastSeen *time.Time, firstAssigned, now time.Time) bool {
	since := now.Add(-inactiveWindow)
	if lastSeen != nil {
		return lastSeen.Before(since)
	}
	return !firstAssigned.IsZero() && firstAssigned.Before(since)
}

// percent — доля в целых процентах.
//
// Пустой знаменатель даёт ноль, а не деление на ноль: у класса без ответов
// доля неопределена, и падать из-за этого экран не должен.
func percent(part, total int) int {
	if total <= 0 {
		return 0
	}
	return int(math.Round(float64(part) / float64(total) * 100))
}

// percentOrNil — та же доля, но с различением «ноль» и «считать не из чего».
//
// Ноль процентов — это результат: ученик отвечал, и всё мимо. Отсутствие
// ответов — не результат, и показывать его нулём значит обвинять человека в
// ошибках, которых он не делал. В JSON это 0 против null, на экране — «0 %»
// против «—».
func percentOrNil(part, total int) *int {
	if total <= 0 {
		return nil
	}
	v := percent(part, total)
	return &v
}

// LessonClassRef — класс, в который выдан урок.
type LessonClassRef struct {
	ID   uuid.UUID
	Name string
}

// LessonClasses отдаёт для каждого урока классы, куда он назначен.
//
// Нужен библиотеке: прежде чем удалить урок, учитель должен увидеть, что тот
// идёт в трёх классах. Поэтому один запрос на весь список — столбец рисуется
// в каждой строке библиотеки, и запрос на строку был бы виден невооружённым
// глазом.
//
// Урок связан с классом через версию: назначается версия, а не урок, иначе
// правка меняла бы уже выданное. Обратного хода «урок → его версии» порт
// уроков не даёт, поэтому идём от назначений: берём пары (версия, класс)
// тенанта и оставляем те, чья версия принадлежит запрошенному уроку. Пар
// столько же, сколько выдач, — это на порядки меньше, чем событий ответов,
// и для школьного каталога это один индексный проход.
func (s *Service) LessonClasses(ctx context.Context, orgID uuid.UUID,
	lessonIDs []uuid.UUID) (map[uuid.UUID][]LessonClassRef, error) {

	out := make(map[uuid.UUID][]LessonClassRef, len(lessonIDs))
	if len(lessonIDs) == 0 {
		return out, nil
	}

	pairs, err := s.store.AssignedClasses(ctx, s.pool, orgID)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать назначения").Wrap(err)
	}
	if len(pairs) == 0 {
		return out, nil
	}

	versionIDs := make([]uuid.UUID, 0, len(pairs))
	seen := make(map[uuid.UUID]bool, len(pairs))
	for _, p := range pairs {
		if seen[p.VersionID] {
			continue
		}
		seen[p.VersionID] = true
		versionIDs = append(versionIDs, p.VersionID)
	}
	versions, err := s.lessons.VersionsInfo(ctx, orgID, versionIDs)
	if err != nil {
		return nil, err
	}

	wanted := make(map[uuid.UUID]bool, len(lessonIDs))
	for _, id := range lessonIDs {
		wanted[id] = true
	}
	byLesson, classIDs := lessonClassIDs(pairs, versions, wanted)
	if len(classIDs) == 0 {
		return out, nil
	}

	// Имена классов — вторым пакетным запросом и только для тех классов,
	// которые действительно попали в ответ.
	classes, err := s.classes.ClassesInfo(ctx, orgID, classIDs)
	if err != nil {
		return nil, err
	}

	for lessonID, ids := range byLesson {
		refs := make([]LessonClassRef, 0, len(ids))
		for _, id := range ids {
			refs = append(refs, LessonClassRef{ID: id, Name: classes[id].Title})
		}
		// По имени, а не как легло из базы: бейджи класса не должны прыгать
		// местами при каждом обновлении страницы.
		sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
		out[lessonID] = refs
	}
	return out, nil
}

// lessonClassIDs раскладывает пары (версия, класс) по урокам.
//
// Возвращает и плоский список классов: имена для них дочитываются одним
// запросом, а не по классу на урок.
func lessonClassIDs(pairs []AssignedClass, versions map[uuid.UUID]VersionInfo,
	wanted map[uuid.UUID]bool) (map[uuid.UUID][]uuid.UUID, []uuid.UUID) {

	byLesson := make(map[uuid.UUID][]uuid.UUID, len(wanted))
	var classIDs []uuid.UUID

	inAnswer := make(map[uuid.UUID]bool, len(pairs))
	placed := make(map[[2]uuid.UUID]bool, len(pairs))
	for _, p := range pairs {
		v, ok := versions[p.VersionID]
		if !ok || !wanted[v.LessonID] {
			continue
		}
		// Две версии одного урока в одном классе — это один класс в ответе:
		// учителю важно, что урок там идёт, а не сколько раз его выдавали.
		key := [2]uuid.UUID{v.LessonID, p.ClassID}
		if placed[key] {
			continue
		}
		placed[key] = true
		byLesson[v.LessonID] = append(byLesson[v.LessonID], p.ClassID)

		if !inAnswer[p.ClassID] {
			inAnswer[p.ClassID] = true
			classIDs = append(classIDs, p.ClassID)
		}
	}
	return byLesson, classIDs
}
