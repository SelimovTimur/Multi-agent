package iam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StudentDirectory — то, что модуль classroom знает об учениках.
//
// Интерфейс объявлен здесь, а реализован сервисом iam. Модуль classroom
// принимает StudentDirectory и не читает схему iam напрямую: это и есть
// граница, которая позволит вынести iam в отдельный сервис без правок в
// вызывающем коде.
type StudentDirectory interface {
	CreateStudent(ctx context.Context, orgID uuid.UUID, displayName, level string) (Student, error)
	ReissueStudentCode(ctx context.Context, orgID, id uuid.UUID) (Student, error)
	StudentByID(ctx context.Context, id uuid.UUID) (Student, error)
	// StudentsByIDs нужен для списков: состав класса — это строки из
	// classroom.enrollments плюс имена из iam.students. Join между схемами
	// запрещён, поэтому имена дочитываются одним пакетным запросом, а не N+1.
	StudentsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Student, error)
}

// Проверка на этапе компиляции, что сервис действительно реализует контракт.
var _ StudentDirectory = (*Service)(nil)

// restoreThrottleKey — псевдоадрес для счётчика попыток входа по личному
// коду. Переиспользует iam.login_attempts, чтобы не заводить вторую таблицу
// с той же семантикой и той же фоновой чисткой.
const restoreThrottleKey = "student:restore"

// Сто неудачных попыток в час с одного адреса. Внешний адрес у школы обычно
// один на всех, а коды дети переписывают с бумажной карточки: при прежних
// десяти несколько опечаток закрывали вход всей школе на час. Перебор при
// 31^8 ≈ 8.5e11 вариантов кода бессмыслен и при сотне попыток.
const maxRestoreAttemptsPerHour = 100

// studentCodeLength — длина личного кода ученика.
const studentCodeLength = 8

// CreateStudent заводит ученика с личным кодом.
//
// Учеников заводит учитель или команда платформы, а не сам ученик, поэтому
// токен устройства здесь не выдаётся: устройство появится, когда ученик
// впервые войдёт по коду с карточки.
//
// displayName — имя ученика так, как его записала школа. Почты, телефона и
// возраста в модели нет.
func (s *Service) CreateStudent(ctx context.Context, orgID uuid.UUID, displayName, level string) (Student, error) {
	displayName = strings.Join(strings.Fields(displayName), " ")
	if displayName == "" {
		return Student{}, httpx.FieldError("display_name", "Укажите имя ученика")
	}
	if len([]rune(displayName)) > 64 {
		return Student{}, httpx.FieldError("display_name", "Слишком длинное имя")
	}

	tag, err := NewPublicTag()
	if err != nil {
		return Student{}, httpx.Internal("Не удалось завести ученика").Wrap(err)
	}

	student := Student{
		ID:          idgen.New(),
		OrgID:       orgID,
		DisplayName: displayName,
		PublicTag:   tag,
		Level:       level,
	}

	// Личный код уникален по платформе. Коллизия маловероятна, но
	// возможна: повторяем несколько раз вместо того, чтобы отдать 500.
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewCode(studentCodeLength)
		if err != nil {
			return Student{}, httpx.Internal("Не удалось завести ученика").Wrap(err)
		}
		student.PublicCode = code

		err = s.store.CreateStudent(ctx, s.pool, student)
		if err == nil {
			return student, nil
		}
		if !pg.IsUniqueViolation(err) {
			return Student{}, httpx.Internal("Не удалось завести ученика").Wrap(err)
		}
		logx.From(ctx).Warn("коллизия личного кода ученика, повторяем", "attempt", attempt+1)
	}
	return Student{}, httpx.Internal("Не удалось завести ученика: не удалось подобрать код")
}

// ReissueStudentCode выдаёт ученику новый личный код.
//
// Нужен, когда карточку потеряли или подсмотрели. Старый код перестаёт
// работать, а все устройства, где ученик уже вошёл, выходят: иначе тот, кто
// вошёл по подсмотренному коду, так и остался бы в чужом аккаунте.
// Код и выход с устройств меняются одной транзакцией — половина этой
// операции хуже, чем ни одной.
func (s *Service) ReissueStudentCode(ctx context.Context, orgID, id uuid.UUID) (Student, error) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewCode(studentCodeLength)
		if err != nil {
			return Student{}, httpx.Internal("Не удалось выдать новый код").Wrap(err)
		}
		err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			if err := s.store.SetStudentCode(ctx, tx, orgID, id, code); err != nil {
				return err
			}
			return s.store.DeleteStudentTokens(ctx, tx, orgID, id)
		})
		if errors.Is(err, ErrNotFound) {
			return Student{}, httpx.NotFound("Ученик не найден")
		}
		if err == nil {
			return s.StudentByID(ctx, id)
		}
		if !pg.IsUniqueViolation(err) {
			return Student{}, httpx.Internal("Не удалось выдать новый код").Wrap(err)
		}
		logx.From(ctx).Warn("коллизия личного кода ученика, повторяем", "attempt", attempt+1)
	}
	return Student{}, httpx.Internal("Не удалось выдать новый код: не удалось подобрать значение")
}

// StudentByID отдаёт ученика.
func (s *Service) StudentByID(ctx context.Context, id uuid.UUID) (Student, error) {
	st, err := s.store.StudentByID(ctx, s.pool, id)
	if errors.Is(err, ErrNotFound) {
		return Student{}, httpx.NotFound("Ученик не найден")
	}
	if err != nil {
		return Student{}, httpx.Internal("Не удалось прочитать профиль").Wrap(err)
	}
	return st, nil
}

// StudentsByIDs отдаёт учеников пачкой. Отсутствующие в базе просто не
// попадают в результат — вызывающий решает, ошибка это или нет.
func (s *Service) StudentsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Student, error) {
	out, err := s.store.StudentsByIDs(ctx, s.pool, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать список учеников").Wrap(err)
	}
	return out, nil
}

// IssueStudentToken выдаёт токен ещё одного устройства.
//
// Ученик держит несколько устройств одновременно: выход на одном не выкидывает
// с других (NR-14). Поэтому токены — отдельные строки, а не одно поле.
func (s *Service) IssueStudentToken(ctx context.Context, st Student, device string) (string, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", httpx.Internal("Не удалось выдать доступ").Wrap(err)
	}
	err = s.store.CreateStudentToken(ctx, s.pool, idgen.New(), st.OrgID, st.ID,
		hash, device, time.Now().Add(s.cfg.StudentTTL))
	if err != nil {
		return "", httpx.Internal("Не удалось выдать доступ").Wrap(err)
	}
	return raw, nil
}

// studentTouchEvery — как часто обновлять last_seen_at ученика. Совпадает с
// порогом в TouchStudent.
const studentTouchEvery = 5 * time.Minute

// AuthenticateStudent разбирает токен устройства в актора.
func (s *Service) AuthenticateStudent(ctx context.Context, rawToken string) (Actor, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Actor{}, httpx.Unauthorized("Нужно войти по личному коду")
	}
	st, err := s.store.StudentByToken(ctx, s.pool, HashToken(rawToken))
	if errors.Is(err, ErrNotFound) {
		return Actor{}, httpx.Unauthorized("Доступ истёк, войди по личному коду заново")
	}
	if err != nil {
		return Actor{}, httpx.Internal("Не удалось проверить доступ").Wrap(err)
	}

	// Отметка активности не должна задерживать ответ ученику. Она нужна раз
	// в 5 минут (тот же порог стоит в самом UPDATE), а время прошлой отметки
	// уже прочитано вместе с токеном: класс, опрашивающий сервер раз в 5
	// секунд, иначе слал бы в базу лишний запрос и горутину на каждый опрос.
	if st.LastSeenAt == nil || time.Since(*st.LastSeenAt) >= studentTouchEvery {
		go func() {
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := s.store.TouchStudent(bg, s.pool, st.ID); err != nil {
				logx.From(bg).Warn("не удалось отметить активность ученика", "err", err)
			}
		}()
	}

	return Actor{
		Kind:      "student",
		StudentID: st.ID,
		OrgID:     st.OrgID,
	}, nil
}

// RestoreStudent впускает ученика по личному коду — единственный способ
// входа ученика.
//
// Код заменяет пароль, поэтому перебор ограничен: не больше сотни неудачных
// попыток в час с одного адреса. Неверный код отдаёт 404 — по ответу нельзя понять,
// существует ли такой код вообще.
func (s *Service) RestoreStudent(ctx context.Context, code, device string, ip *netip.Addr) (Student, string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return Student{}, "", httpx.FieldError("code", "Введи личный код")
	}

	since := time.Now().Add(-time.Hour)
	failed, err := s.store.CountFailedLogins(ctx, s.pool, restoreThrottleKey, ip, since)
	if err != nil {
		return Student{}, "", httpx.Internal("Не удалось восстановить доступ").Wrap(err)
	}
	if failed >= maxRestoreAttemptsPerHour {
		return Student{}, "", httpx.RateLimited("Слишком много попыток. Попробуй через час")
	}

	st, err := s.store.StudentByPublicCode(ctx, s.pool, code)
	if errors.Is(err, ErrNotFound) {
		s.recordAttempt(ctx, restoreThrottleKey, ip, false)
		return Student{}, "", httpx.NotFound("Код не подошёл")
	}
	if err != nil {
		return Student{}, "", httpx.Internal("Не удалось восстановить доступ").Wrap(err)
	}

	token, err := s.IssueStudentToken(ctx, st, device)
	if err != nil {
		return Student{}, "", err
	}
	s.recordAttempt(ctx, restoreThrottleKey, ip, true)
	return st, token, nil
}

// StudentLogout закрывает доступ на текущем устройстве.
func (s *Service) StudentLogout(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	if err := s.store.DeleteStudentToken(ctx, s.pool, HashToken(rawToken)); err != nil {
		return httpx.Internal("Не удалось выйти").Wrap(err)
	}
	return nil
}

// UpdateStudentName меняет отображаемое имя. Меняет его сам ученик, и учитель
// сразу видит новое значение (NR-38).
func (s *Service) UpdateStudentName(ctx context.Context, id uuid.UUID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return httpx.FieldError("display_name", "Укажите, как тебя показывать")
	}
	if len([]rune(name)) > 64 {
		return httpx.FieldError("display_name", "Слишком длинное имя")
	}
	if err := s.store.UpdateStudentProfile(ctx, s.pool, id, name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return httpx.NotFound("Ученик не найден")
		}
		return httpx.Internal("Не удалось сохранить профиль").Wrap(err)
	}
	return nil
}

// Housekeeping чистит протухшие сессии и журнал попыток входа.
//
// Вызывается фоновой задачей: без неё обе таблицы растут неограниченно, а
// инвариант И-5 требует, чтобы потребление ресурсов не зависело от числа
// обслуженных за сутки пользователей.
func (s *Service) Housekeeping(ctx context.Context) error {
	sessions, err := s.store.PurgeExpiredSessions(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("iam: чистка сессий: %w", err)
	}
	attempts, err := s.store.PurgeLoginAttempts(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("iam: чистка попыток входа: %w", err)
	}
	if sessions > 0 || attempts > 0 {
		logx.From(ctx).Info("фоновая чистка iam",
			"sessions_deleted", sessions, "login_attempts_deleted", attempts)
	}
	return nil
}
