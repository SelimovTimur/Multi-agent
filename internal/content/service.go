package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const defaultPageSize = 50

// Assignments — что библиотеке нужно знать о выдаче уроков в классы.
//
// Интерфейс, а не запрос в learning.assignments и classroom.classes: этими
// схемами владеют другие модули, и джойн к ним закрыл бы дорогу к разделению
// на сервисы. Запрет закреплён тестами в internal/arch.
type Assignments interface {
	// HasVersionAssignments — есть ли ссылки на версию, включая закрытые
	// назначения: история ученика должна сохранять прежнее содержимое.
	HasVersionAssignments(ctx context.Context, orgID, versionID uuid.UUID) (bool, error)
	// ClassesForLessons — классы, в которые выдан каждый из уроков.
	//
	// Пачкой на весь список: столбец «назначен в классы» рисуется на каждой
	// строке библиотеки, и запрос на строку здесь виден невооружённым глазом.
	ClassesForLessons(ctx context.Context, orgID uuid.UUID, lessonIDs []uuid.UUID) (map[uuid.UUID][]ClassRef, error)
}

// ClassRef — класс в строке библиотеки: бейдж «7А» и ссылка на него.
type ClassRef struct {
	ID   uuid.UUID
	Name string
}

// Service — прикладная логика библиотеки уроков.
type Service struct {
	pool  *pg.Pool
	store *Store

	// Назначения подключаются отдельно (WithAssignments): без них библиотека
	// работает, просто не показывает, куда урок выдан.
	assignments Assignments

	// Задания уроков и разбор упражнений для учителя — производные версии,
	// которые экран «Урок в классе» спрашивает при каждом опросе. Кеш только
	// экономит запросы: промах всегда читает базу (см. versionCache).
	tasks   *versionCache[[]LessonTask]
	reviews *versionCache[map[uuid.UUID]ExerciseReview]
}

// Пределы кешей. Запись — десяток заданий или три десятка вопросов, так что
// даже полный кеш — единицы мегабайт: столько уроков одновременно идёт в
// крупной школе, а остальное вытесняется.
const (
	taskCacheSize   = 2048
	reviewCacheSize = 512
)

func NewService(pool *pg.Pool) *Service {
	return &Service{
		pool: pool, store: NewStore(),
		tasks:   newVersionCache[[]LessonTask](taskCacheSize),
		reviews: newVersionCache[map[uuid.UUID]ExerciseReview](reviewCacheSize),
	}
}

// WithAssignments подключает сведения о выдаче уроков в классы.
//
// Отдельно от конструктора, а не параметром: модуль назначений сам зависит от
// content, и передать его в NewService значило бы замкнуть круг.
func (s *Service) WithAssignments(a Assignments) *Service {
	s.assignments = a
	return s
}

var allowedLevels = map[string]bool{"A1": true, "A2": true, "B1": true, "B2": true, "C1": true}

// titleLimit — предел названия урока. Один на создание и на переименование:
// разные пределы на одно поле значат, что урок, который нельзя создать,
// можно получить переименованием.
const titleLimit = 200

// tooLongTitle — отказ словами. «Слишком длинное» не говорит, насколько:
// учитель не знает, сократить на десять символов или на сто.
const tooLongTitle = "Название урока не длиннее 200 символов"

// ---------------------------------------------------------------------------
// Создание урока из результата сборки
// ---------------------------------------------------------------------------

// DraftContent — содержимое урока для новой версии: текст, лексика,
// упражнения, цели и настройка разговора. В MVP уроки приходят готовыми
// из cmd/seed (ParseDraft).
type DraftContent struct {
	PreserveContentIDs bool
	Title              string
	LangCode           string
	Level              string
	SourceKind         string
	RefinedText        string
	FrankText          string
	GrammarRaw         string
	// Замечания проверки видны учителю; самостоятельная правка текста
	// не означает, что проверка снова выполнена и замечания устранены.
	QualityWarnings json.RawMessage
	// Note — заметка учителя к версии: чем эта редакция отличается от
	// предыдущей. Видна только ему, ученику не показывается.
	Note      string
	Lexicon   []LexicalUnit
	Grammar   []GrammarTopic
	Exercises []Exercise
	// Objectives — цели урока. nil значит «не трогать», как и у настройки
	// аватара: экраны, которые про цели не знают, их не сбрасывают.
	Objectives []Objective

	// Avatar — настройка разговора с аватаром. nil значит «не трогать»:
	// экраны, которые про аватара не знают (ревью, правка текста), не должны
	// сбрасывать роль, выставленную учителем. Новая версия в этом случае
	// получает настройку предыдущей.
	Avatar *AvatarConfig
}

func (d DraftContent) validate() (DraftContent, error) {
	// Черновик может повторно использоваться вызывающим кодом. Выдача новых
	// ID и нормализация не должны менять его срезы или создавать гонку.
	d.Lexicon = slices.Clone(d.Lexicon)
	d.Grammar = slices.Clone(d.Grammar)
	d.Exercises = slices.Clone(d.Exercises)
	d.Objectives = slices.Clone(d.Objectives)
	d.Title = strings.TrimSpace(d.Title)
	d.LangCode = strings.ToLower(strings.TrimSpace(d.LangCode))
	d.Level = strings.ToUpper(strings.TrimSpace(d.Level))
	if d.SourceKind == "" {
		d.SourceKind = "text"
	}

	fields := map[string]string{}
	if d.Title == "" {
		fields["title"] = "Укажите название урока"
	}
	if len([]rune(d.Title)) > titleLimit {
		fields["title"] = tooLongTitle
	}
	if d.LangCode == "" {
		fields["lang_code"] = "Не указан язык урока"
	}
	if !allowedLevels[d.Level] {
		fields["level"] = "Уровень: A1, A2, B1, B2 или C1"
	}
	// Пределы из NR-27: десять ключевых выражений и три конструкции на урок.
	// Не вкусовщина: выше этого ученик не удержит, а сборка начинает молоть
	// всё подряд. Пайплайн обрезает свой результат до этих же чисел.
	if len(d.Lexicon) > MaxLexicon {
		fields["lexicon"] = fmt.Sprintf("Не больше %d ключевых выражений в уроке", MaxLexicon)
	}
	if len(d.Grammar) > GrammarMaxTopics {
		fields["grammar"] = fmt.Sprintf("Не больше %d конструкций в уроке", GrammarMaxTopics)
	}
	for i := range d.Grammar {
		d.Grammar[i] = normalizeGrammar(d.Grammar[i], fields)
	}
	if d.Objectives != nil {
		d.Objectives = normalizeObjectives(d.Objectives, fields)
	}
	if d.Avatar != nil {
		c := normalizeAvatar(*d.Avatar, fields)
		d.Avatar = &c
	}
	if len(fields) > 0 {
		return d, httpx.Validation("Проверьте данные урока", fields)
	}
	return d, nil
}

// CreateFromDraft заводит урок и его первую версию одной транзакцией.
//
// Отдельного действия «сохранить» у учителя нет: собранный урок попадает в
// библиотеку сразу. Иначе результат тридцатисекундной сборки терялся бы от
// закрытой вкладки, а повторить его стоит денег.
func (s *Service) CreateFromDraft(ctx context.Context, actor iam.Actor, in DraftContent) (Lesson, LessonVersion, error) {
	in, err := in.validate()
	if err != nil {
		return Lesson{}, LessonVersion{}, err
	}

	lesson := Lesson{
		ID:         idgen.New(),
		OrgID:      actor.OrgID,
		AuthorID:   actor.UserID,
		Title:      in.Title,
		LangCode:   in.LangCode,
		Level:      in.Level,
		Status:     LessonDraft,
		SourceKind: in.SourceKind,
	}
	version := LessonVersion{
		ID:              idgen.New(),
		OrgID:           actor.OrgID,
		Status:          VersionDraft,
		Title:           in.Title,
		Level:           in.Level,
		RefinedText:     in.RefinedText,
		FrankText:       in.FrankText,
		GrammarRaw:      in.GrammarRaw,
		QualityWarnings: in.QualityWarnings,
		CreatedBy:       actor.UserID,
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if lesson, err = s.store.CreateLesson(ctx, tx, lesson); err != nil {
			return err
		}
		version.LessonID = lesson.ID
		if version, err = s.store.CreateVersion(ctx, tx, version); err != nil {
			return err
		}
		return s.writeContent(ctx, tx, actor.OrgID, version.ID, freshContent(in))
	})
	if err != nil {
		return Lesson{}, LessonVersion{}, wrapErr(err, "Не удалось сохранить урок")
	}
	telemetry.Emit(ctx, telemetry.Event{Name: "lesson_created", EntityID: lesson.ID, LessonID: lesson.ID})
	return lesson, version, nil
}

// NewVersionFromDraft добавляет уроку новую версию.
//
// Правка не меняет то, что уже выдано: назначения ссылаются на конкретную
// версию, и класс, получивший урок в сентябре, в мае видит сентябрьскую
// редакцию.
func (s *Service) NewVersionFromDraft(ctx context.Context, actor iam.Actor, lessonID uuid.UUID,
	in DraftContent) (LessonVersion, error) {
	in, err := in.validate()
	if err != nil {
		return LessonVersion{}, err
	}
	lesson, err := s.Lesson(ctx, actor, lessonID)
	if err != nil {
		return LessonVersion{}, err
	}

	version := LessonVersion{
		ID:              idgen.New(),
		OrgID:           actor.OrgID,
		LessonID:        lesson.ID,
		Status:          VersionDraft,
		Title:           in.Title,
		Level:           in.Level,
		RefinedText:     in.RefinedText,
		FrankText:       in.FrankText,
		GrammarRaw:      in.GrammarRaw,
		QualityWarnings: in.QualityWarnings,
		CreatedBy:       actor.UserID,
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// MAX(version)+1 сам по себе не сериализует два создания: после
		// ожидания блокировки следующий запрос увидит уже добавленную версию.
		if err := s.store.LockLesson(ctx, tx, actor.OrgID, lesson.ID); err != nil {
			return err
		}
		// Настройку аватара экраны правки текста и ревью не присылают — новая
		// версия получает её от предыдущей, а не сбрасывает роль молча. Так
		// же и с пояснениями к целям: экран без них переносит пояснения
		// прежней версии к тем же целям.
		if in.Avatar == nil || notesMissing(in.Avatar) {
			prev, ok, err := s.store.LatestLessonAvatarConfig(ctx, tx, actor.OrgID, lesson.ID)
			if err != nil {
				return err
			}
			switch {
			case ok && in.Avatar == nil:
				in.Avatar = &prev
			case ok:
				keepNotes(in.Avatar, prev)
			}
		}
		// Разбор грамматики приходит вместе с уроком, кабинет его не видит и
		// обратно не присылает. Новая версия из правки без него потеряла бы
		// разбор насовсем — берём у самой свежей версии, где он есть.
		// Цели переезжают в новую версию так же, как настройка аватара:
		// экраны блоков их не присылают, а терять описание урока при каждой
		// правке текста незачем. Ищем у самой свежей версии, где они есть.
		if in.Objectives == nil {
			versions, err := s.store.ListVersions(ctx, tx, actor.OrgID, lesson.ID)
			if err != nil {
				return err
			}
			for _, v := range versions {
				objectives, err := s.store.ListObjectives(ctx, tx, v.ID)
				if err != nil {
					return err
				}
				if len(objectives) > 0 {
					in.Objectives = objectives
					break
				}
			}
		}
		if version.GrammarRaw == "" || len(version.QualityWarnings) == 0 {
			prev, err := s.store.ListVersions(ctx, tx, actor.OrgID, lesson.ID)
			if err != nil {
				return err
			}
			if len(version.QualityWarnings) == 0 && len(prev) > 0 {
				version.QualityWarnings = prev[0].QualityWarnings
			}
			for _, p := range prev {
				if version.GrammarRaw == "" && p.GrammarRaw != "" {
					version.GrammarRaw = p.GrammarRaw
					break
				}
			}
		}
		var err error
		if version, err = s.store.CreateVersion(ctx, tx, version); err != nil {
			return err
		}
		return s.writeContent(ctx, tx, actor.OrgID, version.ID, freshContent(in))
	})
	if err != nil {
		return LessonVersion{}, wrapErr(err, "Не удалось создать версию")
	}
	return version, nil
}

// writeContent раскладывает содержимое версии по таблицам.
func (s *Service) writeContent(ctx context.Context, tx pgx.Tx, orgID, versionID uuid.UUID, in DraftContent) error {
	for i := range in.Lexicon {
		if in.Lexicon[i].ID == uuid.Nil {
			in.Lexicon[i].ID = idgen.New()
		}
		if in.Lexicon[i].Origin == "" {
			in.Lexicon[i].Origin = OriginModel
		}
	}
	for i := range in.Grammar {
		if in.Grammar[i].ID == uuid.Nil {
			in.Grammar[i].ID = idgen.New()
		}
		if in.Grammar[i].Origin == "" {
			in.Grammar[i].Origin = OriginModel
		}
	}
	for i := range in.Exercises {
		if in.Exercises[i].ID == uuid.Nil {
			in.Exercises[i].ID = idgen.New()
		}
	}

	if err := s.store.ReplaceLexicon(ctx, tx, orgID, versionID, in.Lexicon); err != nil {
		return err
	}
	if err := s.store.ReplaceGrammar(ctx, tx, orgID, versionID, in.Grammar); err != nil {
		return err
	}
	if in.Avatar != nil {
		if err := s.store.UpsertAvatarConfig(ctx, tx, orgID, versionID, *in.Avatar); err != nil {
			return err
		}
	}
	if in.Objectives != nil {
		for i := range in.Objectives {
			if in.Objectives[i].ID == uuid.Nil {
				in.Objectives[i].ID = idgen.New()
			}
		}
		if err := s.store.ReplaceObjectives(ctx, tx, orgID, versionID, in.Objectives); err != nil {
			return err
		}
	}
	return s.store.ReplaceExercises(ctx, tx, orgID, versionID, in.Exercises)
}

// ---------------------------------------------------------------------------
// Чтение
// ---------------------------------------------------------------------------

// Lesson отдаёт урок автора. Чужой — 404, а не 403.
func (s *Service) Lesson(ctx context.Context, actor iam.Actor, id uuid.UUID) (Lesson, error) {
	l, err := s.store.LessonByID(ctx, s.pool, actor.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		return Lesson{}, httpx.NotFound("Урок не найден")
	}
	if err != nil {
		return Lesson{}, httpx.Internal("Не удалось прочитать урок").Wrap(err)
	}
	if l.AuthorID != actor.UserID {
		return Lesson{}, httpx.NotFound("Урок не найден")
	}
	return l, nil
}

// Library отдаёт библиотеку автора.
func (s *Service) Library(ctx context.Context, actor iam.Actor, f LessonFilter) ([]Lesson, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = defaultPageSize
	}
	out, err := s.store.ListLessons(ctx, s.pool, actor.OrgID, actor.UserID, f)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать библиотеку").Wrap(err)
	}
	return out, nil
}

// LibraryRow — строка библиотеки: урок, его последняя версия и классы.
type LibraryRow struct {
	Lesson Lesson
	Latest VersionMark
	// Published — версия, которую сейчас видят классы. Без неё строка
	// библиотеки писала «черновик» об уроке, который прямо сейчас проходят
	// дети: статус брался у самой свежей версии, а не у выданной.
	Published VersionMark
	Classes   []ClassRef
}

// LibraryView собирает библиотеку такой, какой учитель видит её на экране:
// с номером версии и классами, в которые урок выдан.
//
// И версии, и классы дочитываются пачкой на весь список — по запросу на
// строку библиотека из полусотни уроков открывалась бы секундами.
func (s *Service) LibraryView(ctx context.Context, actor iam.Actor, f LessonFilter) ([]LibraryRow, error) {
	lessons, err := s.Library(ctx, actor, f)
	if err != nil {
		return nil, err
	}
	if len(lessons) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, 0, len(lessons))
	for _, l := range lessons {
		ids = append(ids, l.ID)
	}

	published, err := s.store.PublishedVersions(ctx, s.pool, actor.OrgID, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать версии").Wrap(err)
	}
	marks, err := s.store.LatestVersions(ctx, s.pool, actor.OrgID, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать версии уроков").Wrap(err)
	}

	// Назначения живут в чужом модуле. Не подключён — строки остаются без
	// бейджей: библиотека не должна переставать открываться из-за этого.
	classes := map[uuid.UUID][]ClassRef{}
	if s.assignments != nil {
		classes, err = s.assignments.ClassesForLessons(ctx, actor.OrgID, ids)
		if err != nil {
			// А вот ошибку проглатывать нельзя: пустой столбец «назначен в
			// классы» учитель прочитает как «нигде не выдан» и спокойно
			// удалит урок, который идёт в трёх классах.
			return nil, wrapErr(err, "Не удалось прочитать назначения уроков")
		}
	}

	out := make([]LibraryRow, 0, len(lessons))
	for _, l := range lessons {
		out = append(out, LibraryRow{
			Lesson: l, Latest: marks[l.ID], Published: published[l.ID], Classes: classes[l.ID],
		})
	}
	return out, nil
}

func (s *Service) Versions(ctx context.Context, actor iam.Actor, lessonID uuid.UUID) ([]LessonVersion, error) {
	if _, err := s.Lesson(ctx, actor, lessonID); err != nil {
		return nil, err
	}
	out, err := s.store.ListVersions(ctx, s.pool, actor.OrgID, lessonID)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать версии").Wrap(err)
	}
	return out, nil
}

// VersionContent отдаёт версию со всем содержимым.
//
// withAnswers решает, попадёт ли в ответ ключ ответов. Для ручек ученика
// он всегда false, и невыбранное из базы поле утечь не может.
func (s *Service) VersionContent(ctx context.Context, orgID, versionID uuid.UUID,
	withAnswers bool) (VersionContent, error) {
	v, err := s.store.VersionByID(ctx, s.pool, orgID, versionID)
	if errors.Is(err, ErrNotFound) {
		return VersionContent{}, httpx.NotFound("Версия урока не найдена")
	}
	if err != nil {
		return VersionContent{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}

	out := VersionContent{Version: v}
	if out.Lexicon, err = s.store.ListLexicon(ctx, s.pool, versionID); err != nil {
		return VersionContent{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if out.Grammar, err = s.store.ListGrammar(ctx, s.pool, versionID); err != nil {
		return VersionContent{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if out.Exercises, err = s.store.ListExercises(ctx, s.pool, versionID, withAnswers); err != nil {
		return VersionContent{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if out.Avatar, err = s.store.AvatarConfig(ctx, s.pool, orgID, versionID); err != nil {
		return VersionContent{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if out.Objectives, err = s.store.ListObjectives(ctx, s.pool, versionID); err != nil {
		return VersionContent{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	return out, nil
}

// VersionLanguage — язык урока, которому принадлежит версия.
//
// В самой версии языка нет: он у урока, и разговору с аватаром он нужен,
// чтобы не отвечать по-татарски на башкирском уроке. Без прав актора:
// вызывающий уже проверил доступ по попытке ученика.
func (s *Service) VersionLanguage(ctx context.Context, orgID, versionID uuid.UUID) (string, error) {
	v, err := s.store.VersionByID(ctx, s.pool, orgID, versionID)
	if errors.Is(err, ErrNotFound) {
		return "", httpx.NotFound("Версия урока не найдена")
	}
	if err != nil {
		return "", httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	l, err := s.store.LessonByID(ctx, s.pool, orgID, v.LessonID)
	if errors.Is(err, ErrNotFound) {
		return "", httpx.NotFound("Урок не найден")
	}
	if err != nil {
		return "", httpx.Internal("Не удалось прочитать урок").Wrap(err)
	}
	return l.LangCode, nil
}

// Lexicon — лексика версии: выражения с переводом, в порядке урока.
//
// Отдельно от StudentContent: аналитике класса нужна лексика каждого урока,
// и читать ради неё упражнения, темы и настройку разговора незачем.
func (s *Service) Lexicon(ctx context.Context, orgID, versionID uuid.UUID) ([]LexicalUnit, error) {
	// Версия ищется с org_id: lexical_units его не хранит, и без этой
	// проверки чужой идентификатор версии отдал бы чужую лексику.
	if _, err := s.store.VersionByID(ctx, s.pool, orgID, versionID); errors.Is(err, ErrNotFound) {
		return nil, httpx.NotFound("Версия урока не найдена")
	} else if err != nil {
		return nil, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	out, err := s.store.ListLexicon(ctx, s.pool, versionID)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать лексику урока").Wrap(err)
	}
	return out, nil
}

// StudentContent — версия для ученика, без ключа ответов.
//
// Отдельный метод рядом с TeacherVersion намеренно: два вызова с разными
// именами перепутать труднее, чем булев флаг. Ключ ответов здесь не
// выбирается из базы вовсе — утечь ему неоткуда.
func (s *Service) StudentContent(ctx context.Context, orgID, versionID uuid.UUID) (VersionContent, error) {
	return s.VersionContent(ctx, orgID, versionID, false)
}

// TeacherVersion отдаёт версию учителю — с ключом ответов и проверкой прав.
func (s *Service) TeacherVersion(ctx context.Context, actor iam.Actor, versionID uuid.UUID) (VersionContent, error) {
	v, err := s.store.VersionByID(ctx, s.pool, actor.OrgID, versionID)
	if errors.Is(err, ErrNotFound) {
		return VersionContent{}, httpx.NotFound("Версия урока не найдена")
	}
	if err != nil {
		return VersionContent{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if _, err := s.Lesson(ctx, actor, v.LessonID); err != nil {
		return VersionContent{}, err
	}
	return s.VersionContent(ctx, actor.OrgID, versionID, true)
}

// LatestPublished — последняя опубликованная версия. Её предлагают назначить.
func (s *Service) LatestPublished(ctx context.Context, actor iam.Actor, lessonID uuid.UUID) (LessonVersion, error) {
	if _, err := s.Lesson(ctx, actor, lessonID); err != nil {
		return LessonVersion{}, err
	}
	v, err := s.store.LatestPublished(ctx, s.pool, actor.OrgID, lessonID)
	if errors.Is(err, ErrNotFound) {
		return LessonVersion{}, httpx.Conflict("У урока нет опубликованной версии")
	}
	if err != nil {
		return LessonVersion{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Правка и публикация
// ---------------------------------------------------------------------------

// EditVersion правит черновик версии.
//
// Опубликованную версию править нельзя — это обещание, данное классам,
// которым урок уже выдан. Запрет стоит триггером в базе; здесь мы ловим
// его и объясняем, что делать вместо.
func (s *Service) EditVersion(ctx context.Context, actor iam.Actor, versionID uuid.UUID,
	in DraftContent) (LessonVersion, error) {
	current, err := s.store.VersionByID(ctx, s.pool, actor.OrgID, versionID)
	if errors.Is(err, ErrNotFound) {
		return LessonVersion{}, httpx.NotFound("Версия урока не найдена")
	}
	if err != nil {
		return LessonVersion{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if _, err := s.Lesson(ctx, actor, current.LessonID); err != nil {
		return LessonVersion{}, err
	}
	if current.Status == VersionPublished || current.Status == VersionArchived {
		return LessonVersion{}, httpx.Conflict("Версия опубликована или архивирована, создайте новую")
	}
	// Старые назначения могли ссылаться на ready. Новые принимают только
	// published, но ранее выданные версии также нельзя менять задним числом.
	if s.assignments != nil {
		assigned, err := s.assignments.HasVersionAssignments(ctx, actor.OrgID, versionID)
		if err != nil {
			return LessonVersion{}, wrapErr(err, "Не удалось проверить назначения версии")
		}
		if assigned {
			return LessonVersion{}, httpx.Conflict("Версия уже назначена классу, создайте новую")
		}
	}

	in, err = in.validate()
	if err != nil {
		return LessonVersion{}, err
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		locked, err := s.store.LockVersion(ctx, tx, actor.OrgID, versionID)
		if err != nil {
			return err
		}
		if locked.Status == VersionPublished || locked.Status == VersionArchived {
			return httpx.Conflict("Версия опубликована или архивирована, создайте новую")
		}
		// Правка без пояснений к целям (старый клиент, карта урока) оставляет
		// пояснения, уже записанные в этой версии, у тех же целей.
		if notesMissing(in.Avatar) {
			prev, err := s.store.AvatarConfig(ctx, tx, actor.OrgID, versionID)
			if err != nil {
				return err
			}
			keepNotes(in.Avatar, prev)
		}
		if err := s.store.UpdateVersionContent(ctx, tx, actor.OrgID, versionID,
			in.Title, in.Level, in.RefinedText, in.FrankText, in.GrammarRaw, in.Note, in.QualityWarnings); err != nil {
			return err
		}
		if in.PreserveContentIDs {
			if err := s.preserveContentIDs(ctx, tx, versionID, &in); err != nil {
				return err
			}
		}
		return s.writeContent(ctx, tx, actor.OrgID, versionID, in)
	})
	if err != nil {
		if pg.IsRestrictViolation(err) {
			return LessonVersion{}, httpx.Conflict("Версия опубликована, создайте новую")
		}
		return LessonVersion{}, wrapErr(err, "Не удалось сохранить версию")
	}

	telemetry.Emit(ctx, telemetry.Event{Name: "lesson_edited", EntityID: idgen.New(), LessonID: current.LessonID})
	out, err := s.store.VersionByID(ctx, s.pool, actor.OrgID, versionID)
	if err != nil {
		return LessonVersion{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	return out, nil
}

// У точечной правки состав и порядок прежние. Сохраняем идентификаторы,
// чтобы замечание к переводу не меняло ключи остальных заданий урока.
func (s *Service) preserveContentIDs(ctx context.Context, tx pgx.Tx, versionID uuid.UUID, in *DraftContent) error {
	lexicon, err := s.store.ListLexicon(ctx, tx, versionID)
	if err != nil {
		return err
	}
	grammar, err := s.store.ListGrammar(ctx, tx, versionID)
	if err != nil {
		return err
	}
	exercises, err := s.store.ListExercises(ctx, tx, versionID, true)
	if err != nil {
		return err
	}
	objectives, err := s.store.ListObjectives(ctx, tx, versionID)
	if err != nil {
		return err
	}
	if len(lexicon) != len(in.Lexicon) || len(grammar) != len(in.Grammar) || len(exercises) != len(in.Exercises) ||
		(in.Objectives != nil && len(objectives) != len(in.Objectives)) {
		return httpx.Conflict("Состав урока изменился. Обновите страницу перед исправлением замечаний")
	}
	for i := range in.Lexicon {
		in.Lexicon[i].ID, in.Lexicon[i].Origin = lexicon[i].ID, lexicon[i].Origin
	}
	for i := range in.Grammar {
		in.Grammar[i].ID, in.Grammar[i].Origin = grammar[i].ID, grammar[i].Origin
	}
	for i := range in.Exercises {
		if in.Exercises[i].Type != exercises[i].Type {
			return httpx.Conflict("Порядок заданий изменился. Обновите страницу перед исправлением замечаний")
		}
		in.Exercises[i].ID, in.Exercises[i].RawSource = exercises[i].ID, exercises[i].RawSource
	}
	for i := range in.Objectives {
		in.Objectives[i].ID = objectives[i].ID
	}
	return nil
}

// SetVersionStatus переводит версию по статусам.
//
// Недопустимый переход отвечает 409 со списком разрешённых: пользователь
// должен узнать, что можно, а не гадать.
// SetVersionStatus переводит версию по статусам и, при публикации, сохраняет
// название и заметку, которые учитель вписал в форме. nil — не менять.
func (s *Service) SetVersionStatus(ctx context.Context, actor iam.Actor, versionID uuid.UUID,
	next string, title, note *string) (LessonVersion, error) {
	current, err := s.store.VersionByID(ctx, s.pool, actor.OrgID, versionID)
	if errors.Is(err, ErrNotFound) {
		return LessonVersion{}, httpx.NotFound("Версия урока не найдена")
	}
	if err != nil {
		return LessonVersion{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if _, err := s.Lesson(ctx, actor, current.LessonID); err != nil {
		return LessonVersion{}, err
	}

	next = strings.ToLower(strings.TrimSpace(next))
	if !canTransition(current.Status, next) {
		allowed := AllowedNext(current.Status)
		if len(allowed) == 0 {
			return LessonVersion{}, httpx.Conflict(
				fmt.Sprintf("Из статуса «%s» переходов нет", current.Status))
		}
		return LessonVersion{}, httpx.Conflict(fmt.Sprintf(
			"Из статуса «%s» можно перейти только в: %s",
			current.Status, strings.Join(allowed, ", ")))
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		locked, err := s.store.LockVersion(ctx, tx, actor.OrgID, versionID)
		if err != nil {
			return err
		}
		if !canTransition(locked.Status, next) {
			return httpx.Conflict("Статус версии уже изменился. Обновите страницу")
		}
		// Правка и публикация блокируют одну строку версии. Проверка
		// содержимого до блокировки могла пропустить параллельно стёртый блок.
		if next == VersionPublished {
			if err := s.checkPublishable(ctx, tx, versionID); err != nil {
				return err
			}
		}
		if err := s.store.SetVersionStatus(ctx, tx, actor.OrgID, versionID, next, title, note); err != nil {
			return err
		}
		// Публикация версии делает урок опубликованным: в библиотеке важно
		// с одного взгляда видеть, что урок можно выдавать.
		if next == VersionPublished {
			return s.store.SetLessonStatus(ctx, tx, actor.OrgID, current.LessonID, LessonPublished)
		}
		return nil
	})
	if err != nil {
		if pg.IsRestrictViolation(err) {
			return LessonVersion{}, httpx.Conflict("Версия опубликована и неизменяема")
		}
		return LessonVersion{}, wrapErr(err, "Не удалось изменить статус версии")
	}

	if next == VersionPublished {
		telemetry.Emit(ctx, telemetry.Event{Name: "lesson_published", EntityID: versionID, LessonID: current.LessonID})
	}
	out, err := s.store.VersionByID(ctx, s.pool, actor.OrgID, versionID)
	if err != nil {
		return LessonVersion{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	return out, nil
}

// UpdateLesson правит название и уровень урока в библиотеке.
// Версии это не задевает: у каждой своё название на момент создания.
func (s *Service) UpdateLesson(ctx context.Context, actor iam.Actor, id uuid.UUID,
	title, level string) (Lesson, error) {
	lesson, err := s.Lesson(ctx, actor, id)
	if err != nil {
		return Lesson{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = lesson.Title
	}
	// Тот же предел, что и при создании. Без него переименование пропускало
	// название любой длины: в базе колонка text, и урок с названием в триста
	// символов ложился на все экраны, где эта строка стоит рядом с другими.
	if len([]rune(title)) > titleLimit {
		return Lesson{}, httpx.FieldError("title", tooLongTitle)
	}
	level = strings.ToUpper(strings.TrimSpace(level))
	if level == "" {
		level = lesson.Level
	}
	if !allowedLevels[level] {
		return Lesson{}, httpx.FieldError("level", "Уровень: A1, A2, B1, B2 или C1")
	}
	if err := s.store.UpdateLessonMeta(ctx, s.pool, actor.OrgID, id, title, level); err != nil {
		return Lesson{}, httpx.Internal("Не удалось обновить урок").Wrap(err)
	}
	lesson.Title, lesson.Level = title, level
	return lesson, nil
}

// Archive убирает урок из библиотеки, не трогая выданное.
func (s *Service) Archive(ctx context.Context, actor iam.Actor, id uuid.UUID, archived bool) (Lesson, error) {
	lesson, err := s.Lesson(ctx, actor, id)
	if err != nil {
		return Lesson{}, err
	}
	status := LessonDraft
	if archived {
		status = LessonArchived
	} else if _, err := s.store.LatestPublished(ctx, s.pool, actor.OrgID, id); err == nil {
		status = LessonPublished
	}
	if err := s.store.SetLessonStatus(ctx, s.pool, actor.OrgID, id, status); err != nil {
		return Lesson{}, httpx.Internal("Не удалось изменить статус урока").Wrap(err)
	}
	lesson.Status = status
	return lesson, nil
}

// Delete помечает урок удалённым.
//
// Мягко: ответы учеников по уже выданным назначениям сохраняются. Снятие
// самих назначений — забота вызывающего кода, модуль content о них не знает.
func (s *Service) Delete(ctx context.Context, actor iam.Actor, id uuid.UUID) error {
	if _, err := s.Lesson(ctx, actor, id); err != nil {
		return err
	}
	if err := s.store.SoftDeleteLesson(ctx, s.pool, actor.OrgID, id); err != nil {
		return httpx.Internal("Не удалось удалить урок").Wrap(err)
	}
	return nil
}

// VersionsByIDs — версии пачкой, без проверки владения.
//
// Владение проверяет вызывающий: назначения уже отфильтрованы по классу
// учителя или ученика, и повторная проверка на каждую строку списка — это
// запрос на строку вместо одного на список.
func (s *Service) VersionsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]LessonVersion, error) {
	out, err := s.store.VersionsByIDs(ctx, s.pool, orgID, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать версии").Wrap(err)
	}
	return out, nil
}

// checkPublishable — можно ли выдавать эту версию классу.
//
// Проверяются не «все поля заполнены», а ровно то, от чего ученик получает
// экран без смысла: блок «Свой текст» без текста и упражнение без единого
// вопроса. Остальное учитель волен выдавать как есть.
func (s *Service) checkPublishable(ctx context.Context, q pg.Querier, versionID uuid.UUID) error {
	items, err := s.store.ListExercises(ctx, q, versionID, false)
	if err != nil {
		return httpx.Internal("Не удалось прочитать урок").Wrap(err)
	}
	for _, e := range items {
		var payload struct {
			Text      string            `json:"text"`
			Items     []json.RawMessage `json:"items"`
			Questions []json.RawMessage `json:"questions"`
		}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &payload)
		}
		name := e.Title
		if name == "" {
			name = e.Type
		}
		if e.Type == ExerciseCustom && strings.TrimSpace(payload.Text) == "" {
			return httpx.Conflict(fmt.Sprintf(
				"Блок «%s» пустой — заполните его или удалите с карты урока", name))
		}
		if e.Type != ExerciseCustom && len(payload.Items)+len(payload.Questions) == 0 {
			return httpx.Conflict(fmt.Sprintf(
				"В задании «%s» нет ни одного вопроса — заполните его или удалите", name))
		}
	}
	return nil
}

// LessonTitles — названия уроков пачкой.
//
// Нужны модулю назначений: класс и ученик видят название урока, а не
// название версии. Название версии — служебная пометка учителя («после
// замечаний методиста»), и ученику она ни о чём не говорит.
func (s *Service) LessonTitles(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out, err := s.store.LessonTitles(ctx, s.pool, orgID, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать уроки").Wrap(err)
	}
	return out, nil
}

// VersionOwner проверяет, что версия принадлежит уроку этого учителя.
// Нужен модулю назначений: он не ходит в схему content напрямую.
func (s *Service) VersionOwner(ctx context.Context, actor iam.Actor, versionID uuid.UUID) (LessonVersion, error) {
	v, err := s.store.VersionByID(ctx, s.pool, actor.OrgID, versionID)
	if errors.Is(err, ErrNotFound) {
		return LessonVersion{}, httpx.NotFound("Версия урока не найдена")
	}
	if err != nil {
		return LessonVersion{}, httpx.Internal("Не удалось прочитать версию").Wrap(err)
	}
	if _, err := s.Lesson(ctx, actor, v.LessonID); err != nil {
		return LessonVersion{}, err
	}
	return v, nil
}

// wrapErr пропускает наши ошибки как есть, чужие заворачивает в 500.
func wrapErr(err error, msg string) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return err
	}
	return httpx.Internal(msg).Wrap(err)
}

// ---------------------------------------------------------------------------
// Опрос после публикации
// ---------------------------------------------------------------------------

// feedbackImproveLimit — предел свободного ответа «что улучшить». С запасом
// на развёрнутый отзыв, но не на вставленный по ошибке текст урока.
const feedbackImproveLimit = 2000
