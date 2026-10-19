package classroom

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// joinCodeMaxAttempts — сколько раз подбираем код класса при коллизии.
const joinCodeMaxAttempts = 5

// maxStudentsPerAdd — сколько учеников можно завести одним списком. Класс —
// это десятки человек; больше похоже на ошибку вставки, а не на класс.
const maxStudentsPerAdd = 60

// Service — прикладная логика классов.
//
// students — порт в модуль iam. Через него и только через него classroom
// узнаёт что-либо об учениках: прямых запросов в схему iam нет.
type Service struct {
	pool     *pg.Pool
	store    *Store
	students iam.StudentDirectory
}

func NewService(pool *pg.Pool, students iam.StudentDirectory) *Service {
	return &Service{pool: pool, store: NewStore(), students: students}
}

// ---------------------------------------------------------------------------
// Классы: то, что делает учитель
// ---------------------------------------------------------------------------

// ClassInput — поля, которые учитель заполняет на экране класса.
type ClassInput struct {
	Title    string
	Subject  string
	LangCode string
	Level    string
}

// allowedLangs — языки платформы. Тот же список, что у llm_service
// (LANGUAGES в app/languages.py) и у списка на экране создания класса.
var allowedLangs = map[string]bool{
	"english": true,
}

var allowedLevels = map[string]bool{"A1": true, "A2": true, "B1": true, "B2": true, "C1": true}

// validate — проверка при создании класса: язык здесь выбирают, и выбор
// ограничен языками платформы.
func (in ClassInput) validate() (ClassInput, error) { return in.check(true) }

// validateEdit — проверка при правке. Язык класса не меняется, и проверять
// его нечего: класс на языке, который платформа когда-то принимала, иначе
// стало бы нельзя ни переименовать, ни закрыть.
func (in ClassInput) validateEdit() (ClassInput, error) { return in.check(false) }

func (in ClassInput) check(langChosen bool) (ClassInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Subject = strings.TrimSpace(in.Subject)
	in.LangCode = strings.ToLower(strings.TrimSpace(in.LangCode))
	in.Level = strings.ToUpper(strings.TrimSpace(in.Level))

	fields := map[string]string{}
	if in.Title == "" {
		fields["title"] = "Укажите название класса"
	}
	if len([]rune(in.Title)) > 64 {
		fields["title"] = "Слишком длинное название"
	}
	if len([]rune(in.Subject)) > 128 {
		fields["subject"] = "Слишком длинное описание предмета"
	}
	if in.LangCode == "" {
		fields["lang_code"] = "Выберите язык"
	} else if langChosen && !allowedLangs[in.LangCode] {
		// Язык класса определяет язык его уроков, и собрать урок можно только
		// на том, который знает llm_service (Language в schemas.py). Класс на
		// чужом языке заводится молча, а ломается позже и не здесь: на сборке
		// урока, на распознавании речи и на разговоре с аватаром.
		fields["lang_code"] = "Язык пока не поддерживается"
	}
	if !allowedLevels[in.Level] {
		fields["level"] = "Выберите уровень: A1, A2, B1, B2 или C1"
	}
	if len(fields) > 0 {
		return in, httpx.Validation("Проверьте заполнение формы", fields)
	}
	return in, nil
}

// CreateClass заводит класс и выдаёт код присоединения.
func (s *Service) CreateClass(ctx context.Context, actor iam.Actor, in ClassInput) (Class, error) {
	in, err := in.validate()
	if err != nil {
		return Class{}, err
	}

	c := Class{
		ID:           idgen.New(),
		OrgID:        actor.OrgID,
		TeacherID:    actor.UserID,
		Title:        in.Title,
		Subject:      in.Subject,
		LangCode:     in.LangCode,
		DefaultLevel: in.Level,
		Status:       ClassActive,
	}

	// Самостоятельного входа в класс по коду больше нет: учеников заводят
	// учитель или команда платформы, а ученик входит по личному коду. Но
	// колонка join_code обязательна и уникальна, поэтому код по-прежнему
	// выдаётся — просто нигде не принимается, а приём закрыт.
	// Код уникален по всей платформе, поэтому теоретически возможна коллизия.
	// Повторяем несколько раз вместо того, чтобы отдавать ошибку учителю.
	for attempt := 0; attempt < joinCodeMaxAttempts; attempt++ {
		code, err := iam.NewCode(joinCodeLength)
		if err != nil {
			return Class{}, httpx.Internal("Не удалось создать класс").Wrap(err)
		}
		c.JoinCode = code

		created, err := s.store.CreateClass(ctx, s.pool, c)
		if err == nil {
			return created, nil
		}
		if !pg.IsUniqueViolation(err) {
			return Class{}, httpx.Internal("Не удалось создать класс").Wrap(err)
		}
		logx.From(ctx).Warn("коллизия кода класса, повторяем", "attempt", attempt+1)
	}
	return Class{}, httpx.Internal("Не удалось создать класс: не удалось подобрать код")
}

// ClassWithCount — класс плюс число учеников для списка в кабинете.
type ClassWithCount struct {
	Class    Class
	Students int
}

// ListClasses отдаёт классы учителя со счётчиком учеников.
func (s *Service) ListClasses(ctx context.Context, actor iam.Actor, includeArchived bool) ([]ClassWithCount, error) {
	classes, err := s.store.ListClasses(ctx, s.pool, actor.OrgID, actor.UserID, includeArchived)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать классы").Wrap(err)
	}

	ids := make([]uuid.UUID, 0, len(classes))
	for _, c := range classes {
		ids = append(ids, c.ID)
	}
	counts, err := s.store.CountActive(ctx, s.pool, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать классы").Wrap(err)
	}

	out := make([]ClassWithCount, 0, len(classes))
	for _, c := range classes {
		out = append(out, ClassWithCount{Class: c, Students: counts[c.ID]})
	}
	return out, nil
}

// Class отдаёт класс учителя.
//
// Чужой класс — 404, а не 403: по 403 перебором вычисляются чужие
// идентификаторы, и клиент узнаёт о существовании объекта, которого не
// должен видеть (NR-10).
func (s *Service) Class(ctx context.Context, actor iam.Actor, id uuid.UUID) (Class, error) {
	c, err := s.store.ClassByID(ctx, s.pool, actor.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		return Class{}, httpx.NotFound("Класс не найден")
	}
	if err != nil {
		return Class{}, httpx.Internal("Не удалось прочитать класс").Wrap(err)
	}
	if c.TeacherID != actor.UserID {
		return Class{}, httpx.NotFound("Класс не найден")
	}
	return c, nil
}

// StudentCount — сколько активных учеников в классе.
func (s *Service) StudentCount(ctx context.Context, classID uuid.UUID) (int, error) {
	counts, err := s.store.CountActive(ctx, s.pool, []uuid.UUID{classID})
	if err != nil {
		return 0, httpx.Internal("Не удалось посчитать учеников").Wrap(err)
	}
	return counts[classID], nil
}

func (s *Service) UpdateClass(ctx context.Context, actor iam.Actor, id uuid.UUID, in ClassInput) (Class, error) {
	c, err := s.Class(ctx, actor, id)
	if err != nil {
		return Class{}, err
	}
	// Язык класса не меняется: на нём уже сгенерированы уроки и записаны
	// ответы. Смена языка — это новый класс, а не правка старого.
	in.LangCode = c.LangCode
	in, err = in.validateEdit()
	if err != nil {
		return Class{}, err
	}
	if err := s.store.UpdateClass(ctx, s.pool, actor.OrgID, id, in.Title, in.Subject, in.Level); err != nil {
		return Class{}, httpx.Internal("Не удалось обновить класс").Wrap(err)
	}
	c.Title, c.Subject, c.DefaultLevel = in.Title, in.Subject, in.Level
	return c, nil
}

// SetArchived архивирует класс или возвращает его в работу.
func (s *Service) SetArchived(ctx context.Context, actor iam.Actor, id uuid.UUID, archived bool) (Class, error) {
	c, err := s.Class(ctx, actor, id)
	if err != nil {
		return Class{}, err
	}
	status := ClassActive
	if archived {
		status = ClassArchived
	}
	if err := s.store.SetClassStatus(ctx, s.pool, actor.OrgID, id, status); err != nil {
		return Class{}, httpx.Internal("Не удалось изменить статус класса").Wrap(err)
	}
	c.Status = status
	return c, nil
}

// ClassesByIDs отдаёт классы пачкой по идентификаторам.
//
// Без актора намеренно, и это не дыра: имена классов внутри одной организации
// не секрет, а связать урок с классом мог только тот, у кого есть доступ и к
// уроку, и к классу. Тенант при этом проверяется всегда — org_id обязателен,
// и чужой класс не найдётся даже по известному идентификатору.
//
// Проверку «мой ли это класс» здесь сделать нельзя: вызывающий спрашивает про
// классы, в которые выдан урок, а выдать урок в класс коллеги — законно.
func (s *Service) ClassesByIDs(ctx context.Context, orgID uuid.UUID,
	ids []uuid.UUID) (map[uuid.UUID]Class, error) {
	out, err := s.store.ClassesByIDs(ctx, s.pool, orgID, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать классы").Wrap(err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Состав класса
// ---------------------------------------------------------------------------

// Roster отдаёт состав класса: записи из classroom плюс имена из iam.
//
// Два запроса вместо join'а между схемами — цена границы модулей. Второй
// запрос пакетный, поэтому на размер класса сложность не растёт.
func (s *Service) Roster(ctx context.Context, actor iam.Actor, classID uuid.UUID,
	includeRemoved bool) ([]Member, error) {
	if _, err := s.Class(ctx, actor, classID); err != nil {
		return nil, err
	}

	enrollments, err := s.store.ListEnrollments(ctx, s.pool, actor.OrgID, classID, includeRemoved)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать состав класса").Wrap(err)
	}
	if len(enrollments) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, 0, len(enrollments))
	for _, e := range enrollments {
		ids = append(ids, e.StudentID)
	}
	students, err := s.students.StudentsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]Member, 0, len(enrollments))
	for _, e := range enrollments {
		st, ok := students[e.StudentID]
		if !ok {
			// Профиль удалён, а запись осталась: показываем строку без имени,
			// а не прячем ученика из состава — иначе счётчики разъедутся.
			logx.From(ctx).Warn("в составе класса есть запись без профиля ученика",
				"class_id", classID, "student_id", e.StudentID)
		}
		out = append(out, Member{
			Enrollment:  e,
			DisplayName: st.DisplayName,
			PublicTag:   st.PublicTag,
			PublicCode:  st.PublicCode,
			Level:       st.Level,
			LastSeenAt:  st.LastSeenAt,
		})
	}
	return out, nil
}

// RemoveStudent мягко исключает ученика: доступ закрыт, ответы сохранены.
func (s *Service) RemoveStudent(ctx context.Context, actor iam.Actor, classID, studentID uuid.UUID) error {
	if _, err := s.Class(ctx, actor, classID); err != nil {
		return err
	}
	err := s.store.SetEnrollmentStatus(ctx, s.pool, actor.OrgID, classID, studentID, EnrollmentRemoved)
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("Ученик не найден в этом классе")
	}
	if err != nil {
		return httpx.Internal("Не удалось исключить ученика").Wrap(err)
	}
	return nil
}

// RestoreStudent возвращает исключённого ученика в класс.
func (s *Service) RestoreStudent(ctx context.Context, actor iam.Actor, classID, studentID uuid.UUID) error {
	if _, err := s.Class(ctx, actor, classID); err != nil {
		return err
	}
	err := s.store.SetEnrollmentStatus(ctx, s.pool, actor.OrgID, classID, studentID, EnrollmentActive)
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("Ученик не найден в этом классе")
	}
	if err != nil {
		return httpx.Internal("Не удалось вернуть ученика").Wrap(err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Заведение учеников
// ---------------------------------------------------------------------------

// AddResult — итог заведения учеников списком.
type AddResult struct {
	// Added — заведённые сейчас, в порядке списка.
	Added []Member
	// Skipped — имена, которые в классе уже есть. Повторный запуск того же
	// списка поэтому ничего не дублирует: импорт можно безопасно повторить
	// после сбоя на середине.
	Skipped []string
}

// AddStudents заводит учеников по списку имён и записывает их в класс.
//
// Каждому выдаётся личный код: им ученик и входит. Самостоятельного входа по
// коду класса нет — школа отдаёт списки, а учитель раздаёт карточки с кодами.
//
// Имя, которое в классе уже есть (без учёта регистра и лишних пробелов),
// пропускается. Исключённые тоже считаются: учитель исключил ученика
// намеренно, и повторный импорт не должен молча вернуть его новым профилем.
//
// Транзакции между заведением профиля в iam и записью в класс нет: это два
// модуля, и общая транзакция сломала бы границу между ними. Сбой на середине
// оставляет часть списка заведённой, а повторный запуск дозаводит остальных.
func (s *Service) AddStudents(ctx context.Context, actor iam.Actor, classID uuid.UUID,
	names []string) (AddResult, error) {
	class, err := s.Class(ctx, actor, classID)
	if err != nil {
		return AddResult{}, err
	}
	if class.Status != ClassActive {
		return AddResult{}, httpx.Conflict("Класс в архиве — верните его в работу, чтобы добавить учеников")
	}

	clean := make([]string, 0, len(names))
	for _, n := range names {
		if n = normalizeName(n); n != "" {
			clean = append(clean, n)
		}
	}
	if len(clean) == 0 {
		return AddResult{}, httpx.FieldError("names", "Впишите хотя бы одно имя")
	}
	if len(clean) > maxStudentsPerAdd {
		return AddResult{}, httpx.FieldError("names",
			fmt.Sprintf("За один раз можно добавить не больше %d учеников", maxStudentsPerAdd))
	}

	existing, err := s.Roster(ctx, actor, classID, true)
	if err != nil {
		return AddResult{}, err
	}
	seen := make(map[string]bool, len(existing)+len(clean))
	for _, m := range existing {
		seen[NameKey(m.DisplayName)] = true
	}

	var res AddResult
	for _, name := range clean {
		key := NameKey(name)
		if seen[key] {
			res.Skipped = append(res.Skipped, name)
			continue
		}
		seen[key] = true

		// Ученик заводится в организации класса: тенант проставляется в
		// момент создания и дальше не меняется.
		student, err := s.students.CreateStudent(ctx, class.OrgID, name, class.DefaultLevel)
		if err != nil {
			return res, err
		}
		e, err := s.store.UpsertEnrollment(ctx, s.pool, Enrollment{
			ID:        idgen.New(),
			OrgID:     class.OrgID,
			ClassID:   class.ID,
			StudentID: student.ID,
			TeacherID: class.TeacherID,
		})
		if err != nil {
			return res, httpx.Internal("Не удалось записать ученика в класс").Wrap(err)
		}
		res.Added = append(res.Added, Member{
			Enrollment:  e,
			DisplayName: student.DisplayName,
			PublicTag:   student.PublicTag,
			PublicCode:  student.PublicCode,
			Level:       student.Level,
			LastSeenAt:  student.LastSeenAt,
		})
	}
	return res, nil
}

// ReissueCode выдаёт ученику класса новый личный код.
//
// Сначала проверяется, что ученик записан именно в этот класс учителя:
// иначе по известному идентификатору можно было бы сбросить код чужому
// ученику той же школы.
func (s *Service) ReissueCode(ctx context.Context, actor iam.Actor, classID, studentID uuid.UUID) (Member, error) {
	if _, err := s.Class(ctx, actor, classID); err != nil {
		return Member{}, err
	}
	e, err := s.store.EnrollmentOf(ctx, s.pool, actor.OrgID, classID, studentID)
	if errors.Is(err, ErrNotFound) {
		return Member{}, httpx.NotFound("Ученик не найден в этом классе")
	}
	if err != nil {
		return Member{}, httpx.Internal("Не удалось выдать новый код").Wrap(err)
	}
	st, err := s.students.ReissueStudentCode(ctx, actor.OrgID, studentID)
	if err != nil {
		return Member{}, err
	}
	return Member{
		Enrollment:  e,
		DisplayName: st.DisplayName,
		PublicTag:   st.PublicTag,
		PublicCode:  st.PublicCode,
		Level:       st.Level,
		LastSeenAt:  st.LastSeenAt,
	}, nil
}

// normalizeName убирает лишние пробелы и табуляции: списки приходят из
// Word и Excel, и «Гарипова  Ясмина» с двумя пробелами — та же Ясмина.
func normalizeName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

// NameKey — ключ сравнения имён внутри класса: регистр, лишние пробелы и
// «ё» не важны. Экспортирован для cmd/roster: план импорта обязан считать
// «уже есть» ровно так же, как AddStudents.
func NameKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(normalizeName(name)), "ё", "е")
}

// MyClasses отдаёт классы ученика.
func (s *Service) MyClasses(ctx context.Context, actor iam.Actor) ([]Class, error) {
	classes, err := s.store.ClassesOfStudent(ctx, s.pool, actor.StudentID)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать классы").Wrap(err)
	}
	return classes, nil
}
