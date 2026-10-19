package iam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// ErrNotFound — объект не найден. Сервис превращает его в 404.
var ErrNotFound = errors.New("iam: не найдено")

// Store — доступ к схеме iam.
//
// Каждый метод принимает pg.Querier первым аргументом после контекста:
// один и тот же код работает и на пуле, и внутри транзакции, без дублей
// вида «...Tx». Регистрация пользователя, например, создаёт организацию и
// пользователя одной транзакцией — иначе при сбое остаётся организация-сирота.
type Store struct{}

// NewStore создаёт доступ к схеме. Состояния у него нет: соединение приходит
// параметром, поэтому Store безопасно использовать из нескольких горутин.
func NewStore() *Store { return &Store{} }

// NormalizeEmail приводит адрес к каноническому виду для поиска и уникальности.
// Регистр и пробелы не должны создавать два аккаунта на один адрес.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ---------------------------------------------------------------------------
// Организации
// ---------------------------------------------------------------------------

func (s *Store) CreateOrg(ctx context.Context, q pg.Querier, o Organization) error {
	_, err := q.Exec(ctx, `
		INSERT INTO iam.organizations (id, kind, title, status)
		VALUES ($1, $2, $3, $4)`,
		o.ID, o.Kind, o.Title, o.Status)
	if err != nil {
		return fmt.Errorf("iam: не удалось создать организацию: %w", err)
	}
	return nil
}

func (s *Store) OrgByID(ctx context.Context, q pg.Querier, id uuid.UUID) (Organization, error) {
	var o Organization
	err := q.QueryRow(ctx, `
		SELECT id, kind, title, status, created_at
		FROM iam.organizations WHERE id = $1`, id).
		Scan(&o.ID, &o.Kind, &o.Title, &o.Status, &o.CreatedAt)
	if pg.IsNoRows(err) {
		return Organization{}, ErrNotFound
	}
	if err != nil {
		return Organization{}, fmt.Errorf("iam: не удалось прочитать организацию: %w", err)
	}
	return o, nil
}

// ---------------------------------------------------------------------------
// Пользователи
// ---------------------------------------------------------------------------

const userColumns = `id, org_id, email, display_name, role_code, status, origin,
	password_hash, email_verified_at, last_login_at, created_at`

func scanUser(row pg.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.DisplayName, &u.Role, &u.Status,
		&u.Origin, &u.PasswordHash, &u.EmailVerifiedAt, &u.LastLoginAt, &u.CreatedAt)
	return u, err
}

func (s *Store) CreateUser(ctx context.Context, q pg.Querier, u User) error {
	_, err := q.Exec(ctx, `
		INSERT INTO iam.users
			(id, org_id, email, email_normalized, password_hash, display_name,
			 role_code, status, origin)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		u.ID, u.OrgID, u.Email, NormalizeEmail(u.Email), u.PasswordHash,
		u.DisplayName, u.Role, u.Status, u.Origin)
	if err != nil {
		return fmt.Errorf("iam: не удалось создать пользователя: %w", err)
	}
	return nil
}

func (s *Store) UserByEmail(ctx context.Context, q pg.Querier, email string) (User, error) {
	row := q.QueryRow(ctx, `SELECT `+userColumns+`
		FROM iam.users WHERE email_normalized = $1 AND deleted_at IS NULL`,
		NormalizeEmail(email))
	u, err := scanUser(row)
	if pg.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("iam: не удалось прочитать пользователя: %w", err)
	}
	return u, nil
}

func (s *Store) UserByID(ctx context.Context, q pg.Querier, id uuid.UUID) (User, error) {
	row := q.QueryRow(ctx, `SELECT `+userColumns+`
		FROM iam.users WHERE id = $1 AND deleted_at IS NULL`, id)
	u, err := scanUser(row)
	if pg.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("iam: не удалось прочитать пользователя: %w", err)
	}
	return u, nil
}

func (s *Store) SetUserStatus(ctx context.Context, q pg.Querier, id uuid.UUID, status string) error {
	tag, err := q.Exec(ctx, `UPDATE iam.users SET status = $2 WHERE id = $1 AND deleted_at IS NULL`,
		id, status)
	if err != nil {
		return fmt.Errorf("iam: не удалось сменить статус пользователя: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// statusesOpenToTransition — из каких статусов ссылка из письма может двигать
// учётку.
//
// Здесь НЕТ blocked и deleted, и это главное в списке: иначе заблокированный
// аккаунт разблокировал бы себя сам — перейдя по старой ссылке подтверждения
// или запросив «забыли пароль». Блокировку снимает человек, а не письмо.
// Закреплено тестом TestBlockedUserCannotBeRevivedByEmailLink.
var statusesOpenToTransition = []string{StatusPending, StatusAwaitingApproval}

// ConfirmEmail отмечает почту подтверждённой и ставит учётку в очередь на
// одобрение. Возвращает false, если двигать было нечего: аккаунт уже активен,
// заблокирован или удалён — это не ошибка, а нормальный исход перехода по
// старой ссылке.
func (s *Store) ConfirmEmail(ctx context.Context, q pg.Querier, id uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE iam.users
		SET status = 'awaiting_approval', email_verified_at = COALESCE(email_verified_at, now())
		WHERE id = $1 AND deleted_at IS NULL AND status = ANY($2)`,
		id, statusesOpenToTransition)
	if err != nil {
		return false, fmt.Errorf("iam: не удалось подтвердить почту: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ActivateUser открывает вход: почта подтверждена, заявка одобрена.
//
// Возвращает false, когда статус не позволял активацию. Ровно это и защищает
// заблокированный аккаунт: запрос не находит строку и молча ничего не делает.
func (s *Store) ActivateUser(ctx context.Context, q pg.Querier, id uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE iam.users
		SET status = 'active',
		    email_verified_at = COALESCE(email_verified_at, now()),
		    approved_at = COALESCE(approved_at, now())
		WHERE id = $1 AND deleted_at IS NULL AND status = ANY($2)`,
		id, statusesOpenToTransition)
	if err != nil {
		return false, fmt.Errorf("iam: не удалось активировать пользователя: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RejectUser закрывает вход отклонённой заявке.
//
// blocked, а не deleted: человек мог ошибиться формой, и поддержке нужно
// видеть, что заявка была и чем кончилась. Удаление — операция без отката.
func (s *Store) RejectUser(ctx context.Context, q pg.Querier, id uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE iam.users SET status = 'blocked'
		WHERE id = $1 AND deleted_at IS NULL AND status = ANY($2)`,
		id, statusesOpenToTransition)
	if err != nil {
		return false, fmt.Errorf("iam: не удалось отклонить заявку: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// LockUser берёт блокировку строки пользователя до конца транзакции.
//
// Нужна там, где решение принимается по прочитанному состоянию: реплик API
// две, и без блокировки обе одновременно прочитают «письмо давно не уходило»
// и обе его отправят. Блокировка строки пользователя — самый дешёвый общий
// замок: операций с ней единицы в день.
func (s *Store) LockUser(ctx context.Context, q pg.Querier, id uuid.UUID) error {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM iam.users WHERE id = $1 FOR UPDATE`, id).Scan(&one)
	if pg.IsNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("iam: не удалось заблокировать пользователя: %w", err)
	}
	return nil
}

func (s *Store) UpdatePassword(ctx context.Context, q pg.Querier, id uuid.UUID, hash string) error {
	tag, err := q.Exec(ctx, `UPDATE iam.users SET password_hash = $2 WHERE id = $1 AND deleted_at IS NULL`,
		id, hash)
	if err != nil {
		return fmt.Errorf("iam: не удалось обновить пароль: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateDisplayName(ctx context.Context, q pg.Querier, id uuid.UUID, name string) error {
	tag, err := q.Exec(ctx, `UPDATE iam.users SET display_name = $2 WHERE id = $1 AND deleted_at IS NULL`,
		id, name)
	if err != nil {
		return fmt.Errorf("iam: не удалось обновить имя: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchLastLogin(ctx context.Context, q pg.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE iam.users SET last_login_at = now() WHERE id = $1`, id)
	return err
}

// ---------------------------------------------------------------------------
// Сессии
// ---------------------------------------------------------------------------

func (s *Store) CreateSession(ctx context.Context, q pg.Querier, sess Session, tokenHash []byte,
	userAgent string, ip *netip.Addr) error {
	var impersonatedBy uuid.NullUUID
	if sess.ImpersonatedBy != nil {
		impersonatedBy = uuid.NullUUID{UUID: *sess.ImpersonatedBy, Valid: true}
	}
	_, err := q.Exec(ctx, `
		INSERT INTO iam.sessions
			(id, org_id, user_id, token_hash, impersonated_by, read_only,
			 user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		sess.ID, sess.OrgID, sess.UserID, tokenHash, impersonatedBy, sess.ReadOnly,
		truncate(userAgent, 512), ip, sess.ExpiresAt)
	if err != nil {
		return fmt.Errorf("iam: не удалось создать сессию: %w", err)
	}
	return nil
}

// SessionByToken возвращает живую сессию и её пользователя одним запросом.
//
// Это единственное место, где обе таблицы читаются вместе, и обе принадлежат
// схеме iam — правило «модуль не ходит в чужую схему» не нарушается.
func (s *Store) SessionByToken(ctx context.Context, q pg.Querier, tokenHash []byte) (Session, User, error) {
	var sess Session
	var u User
	var impersonatedBy uuid.NullUUID

	err := q.QueryRow(ctx, `
		SELECT s.id, s.org_id, s.user_id, s.impersonated_by, s.read_only,
		       s.expires_at, s.last_seen_at,
		       u.id, u.org_id, u.email, u.display_name, u.role_code, u.status,
		       u.origin, u.password_hash, u.email_verified_at, u.last_login_at, u.created_at
		FROM iam.sessions s
		JOIN iam.users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.deleted_at IS NULL`,
		tokenHash).
		Scan(&sess.ID, &sess.OrgID, &sess.UserID, &impersonatedBy, &sess.ReadOnly,
			&sess.ExpiresAt, &sess.LastSeenAt,
			&u.ID, &u.OrgID, &u.Email, &u.DisplayName, &u.Role, &u.Status,
			&u.Origin, &u.PasswordHash, &u.EmailVerifiedAt, &u.LastLoginAt, &u.CreatedAt)
	if pg.IsNoRows(err) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("iam: не удалось прочитать сессию: %w", err)
	}
	if impersonatedBy.Valid {
		id := impersonatedBy.UUID
		sess.ImpersonatedBy = &id
	}
	return sess, u, nil
}

// TouchSession продлевает сессию при активности.
//
// Запись делается не чаще раза в час: иначе каждый запрос ученика превращается
// в UPDATE и таблица сессий становится самой горячей в базе.
func (s *Store) TouchSession(ctx context.Context, q pg.Querier, id uuid.UUID, ttl time.Duration) error {
	_, err := q.Exec(ctx, `
		UPDATE iam.sessions
		SET last_seen_at = now(), expires_at = now() + $2::interval
		WHERE id = $1 AND last_seen_at < now() - interval '1 hour'`,
		id, ttl.String())
	return err
}

func (s *Store) DeleteSession(ctx context.Context, q pg.Querier, tokenHash []byte) error {
	_, err := q.Exec(ctx, `DELETE FROM iam.sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteUserSessions завершает все сессии пользователя, кроме указанной.
// Вызывается при смене пароля: «остальные устройства выходят из аккаунта».
func (s *Store) DeleteUserSessions(ctx context.Context, q pg.Querier, userID uuid.UUID, except uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM iam.sessions WHERE user_id = $1 AND id <> $2`, userID, except)
	return err
}

// PurgeExpiredSessions чистит протухшие сессии. Вызывается фоновой задачей:
// без неё таблица растёт неограниченно (инвариант И-5).
func (s *Store) PurgeExpiredSessions(ctx context.Context, q pg.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM iam.sessions WHERE expires_at < now() - interval '7 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Одноразовые токены писем
// ---------------------------------------------------------------------------

func (s *Store) CreateEmailToken(ctx context.Context, q pg.Querier, id, orgID, userID uuid.UUID,
	kind string, hash []byte, expiresAt time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO iam.email_tokens (id, org_id, user_id, kind, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, orgID, userID, kind, hash, expiresAt)
	if err != nil {
		return fmt.Errorf("iam: не удалось создать токен письма: %w", err)
	}
	return nil
}

// ConsumeEmailToken помечает токен использованным и возвращает пользователя.
//
// Пометка и проверка идут одним UPDATE ... RETURNING: два параллельных
// перехода по одной ссылке не должны оба сработать.
func (s *Store) ConsumeEmailToken(ctx context.Context, q pg.Querier, hash []byte, kind string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := q.QueryRow(ctx, `
		UPDATE iam.email_tokens
		SET used_at = now()
		WHERE token_hash = $1 AND kind = $2 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`, hash, kind).Scan(&userID)
	if pg.IsNoRows(err) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("iam: не удалось погасить токен: %w", err)
	}
	return userID, nil
}

// LastEmailTokenAt отдаёт время последнего выданного токена этого вида.
//
// Это же и время последнего письма: токен создаётся ровно тогда, когда письмо
// уходит. Отдельной таблицы «когда мы писали» поэтому не нужно — она хранила
// бы то же самое и могла бы с этим разойтись.
func (s *Store) LastEmailTokenAt(ctx context.Context, q pg.Querier, userID uuid.UUID,
	kind string) (time.Time, bool, error) {
	var at time.Time
	err := q.QueryRow(ctx, `
		SELECT created_at FROM iam.email_tokens
		WHERE user_id = $1 AND kind = $2
		ORDER BY created_at DESC LIMIT 1`, userID, kind).Scan(&at)
	if pg.IsNoRows(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("iam: не удалось прочитать время последнего письма: %w", err)
	}
	return at, true, nil
}

// ApprovalByTokenHash читает заявку по токену, НЕ гася его.
//
// Отдельный метод от ConsumeEmailToken намеренно: страница одобрения
// открывается по ссылке из письма, а GET не имеет права менять состояние —
// почтовые антивирусы и превьюшки мессенджеров ходят по ссылкам сами и
// «принимали» бы решение за человека.
//
// Обе таблицы принадлежат схеме iam, правило «модуль не ходит в чужую схему»
// не нарушено.
func (s *Store) ApprovalByTokenHash(ctx context.Context, q pg.Querier, hash []byte) (ApprovalRequest, error) {
	var req ApprovalRequest
	err := q.QueryRow(ctx, `
		SELECT u.id, u.display_name, u.email, o.title, u.created_at, u.status,
		       t.used_at IS NOT NULL
		FROM iam.email_tokens t
		JOIN iam.users u ON u.id = t.user_id
		JOIN iam.organizations o ON o.id = t.org_id
		WHERE t.token_hash = $1 AND t.kind = $2 AND t.expires_at > now()
		  AND u.deleted_at IS NULL`, hash, tokenKindApprove).
		Scan(&req.UserID, &req.Name, &req.Email, &req.Org, &req.RequestedAt, &req.Status, &req.Decided)
	if pg.IsNoRows(err) {
		return ApprovalRequest{}, ErrNotFound
	}
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("iam: не удалось прочитать заявку: %w", err)
	}
	return req, nil
}

// InvalidateEmailTokens гасит неиспользованные токены выбранного вида.
// Нужно, чтобы старая ссылка восстановления перестала работать после новой.
func (s *Store) InvalidateEmailTokens(ctx context.Context, q pg.Querier, userID uuid.UUID, kind string) error {
	_, err := q.Exec(ctx, `
		UPDATE iam.email_tokens SET used_at = now()
		WHERE user_id = $1 AND kind = $2 AND used_at IS NULL`, userID, kind)
	return err
}

// ---------------------------------------------------------------------------
// Попытки входа
// ---------------------------------------------------------------------------

func (s *Store) RecordLoginAttempt(ctx context.Context, q pg.Querier, email string, ip *netip.Addr, ok bool) error {
	_, err := q.Exec(ctx, `
		INSERT INTO iam.login_attempts (email_normalized, ip, succeeded)
		VALUES ($1, $2, $3)`, NormalizeEmail(email), ip, ok)
	return err
}

// CountFailedLogins считает неудачные попытки по паре (email, IP) за окно.
func (s *Store) CountFailedLogins(ctx context.Context, q pg.Querier, email string,
	ip *netip.Addr, since time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM iam.login_attempts
		WHERE email_normalized = $1
		  AND ip IS NOT DISTINCT FROM $2
		  AND succeeded = false
		  AND occurred_at > $3`, NormalizeEmail(email), ip, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("iam: не удалось посчитать попытки входа: %w", err)
	}
	return n, nil
}

// PurgeLoginAttempts чистит журнал попыток: он нужен только на окно блокировки.
func (s *Store) PurgeLoginAttempts(ctx context.Context, q pg.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM iam.login_attempts WHERE occurred_at < now() - interval '2 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Ученики
// ---------------------------------------------------------------------------

func (s *Store) CreateStudent(ctx context.Context, q pg.Querier, st Student) error {
	_, err := q.Exec(ctx, `
		INSERT INTO iam.students (id, org_id, display_name, public_tag, public_code, level)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		st.ID, st.OrgID, st.DisplayName, st.PublicTag, st.PublicCode, nullIfEmpty(st.Level))
	if err != nil {
		return fmt.Errorf("iam: не удалось создать ученика: %w", err)
	}
	return nil
}

const studentColumns = `id, org_id, display_name, public_tag, public_code,
	COALESCE(level, ''), created_at, last_seen_at`

func scanStudent(row pg.Row) (Student, error) {
	var st Student
	err := row.Scan(&st.ID, &st.OrgID, &st.DisplayName, &st.PublicTag, &st.PublicCode,
		&st.Level, &st.CreatedAt, &st.LastSeenAt)
	return st, err
}

func (s *Store) StudentByID(ctx context.Context, q pg.Querier, id uuid.UUID) (Student, error) {
	st, err := scanStudent(q.QueryRow(ctx, `SELECT `+studentColumns+` FROM iam.students WHERE id = $1`, id))
	if pg.IsNoRows(err) {
		return Student{}, ErrNotFound
	}
	if err != nil {
		return Student{}, fmt.Errorf("iam: не удалось прочитать ученика: %w", err)
	}
	return st, nil
}

func (s *Store) StudentsByIDs(ctx context.Context, q pg.Querier, ids []uuid.UUID) (map[uuid.UUID]Student, error) {
	out := make(map[uuid.UUID]Student, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx,
		`SELECT `+studentColumns+` FROM iam.students WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("iam: не удалось прочитать учеников: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		st, err := scanStudent(rows)
		if err != nil {
			return nil, fmt.Errorf("iam: не удалось прочитать учеников: %w", err)
		}
		out[st.ID] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: не удалось прочитать учеников: %w", err)
	}
	return out, nil
}

func (s *Store) StudentByPublicCode(ctx context.Context, q pg.Querier, code string) (Student, error) {
	st, err := scanStudent(q.QueryRow(ctx,
		`SELECT `+studentColumns+` FROM iam.students WHERE public_code = $1`,
		strings.ToUpper(strings.TrimSpace(code))))
	if pg.IsNoRows(err) {
		return Student{}, ErrNotFound
	}
	if err != nil {
		return Student{}, fmt.Errorf("iam: не удалось прочитать ученика по коду: %w", err)
	}
	return st, nil
}

func (s *Store) UpdateStudentProfile(ctx context.Context, q pg.Querier, id uuid.UUID, name string) error {
	tag, err := q.Exec(ctx, `UPDATE iam.students SET display_name = $2 WHERE id = $1`, id, name)
	if err != nil {
		return fmt.Errorf("iam: не удалось обновить профиль ученика: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStudentCode меняет личный код ученика.
func (s *Store) SetStudentCode(ctx context.Context, q pg.Querier, orgID, id uuid.UUID, code string) error {
	tag, err := q.Exec(ctx, `UPDATE iam.students SET public_code = $3 WHERE id = $1 AND org_id = $2`,
		id, orgID, code)
	if err != nil {
		return fmt.Errorf("iam: не удалось сменить код ученика: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchStudent(ctx context.Context, q pg.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `
		UPDATE iam.students SET last_seen_at = now()
		WHERE id = $1 AND (last_seen_at IS NULL OR last_seen_at < now() - interval '5 minutes')`, id)
	return err
}

// ---------------------------------------------------------------------------
// Токены устройств ученика
// ---------------------------------------------------------------------------

func (s *Store) CreateStudentToken(ctx context.Context, q pg.Querier, id, orgID, studentID uuid.UUID,
	hash []byte, device string, expiresAt time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO iam.student_tokens (id, org_id, student_id, token_hash, device_label, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, orgID, studentID, hash, truncate(device, 128), expiresAt)
	if err != nil {
		return fmt.Errorf("iam: не удалось выдать токен ученику: %w", err)
	}
	return nil
}

// StudentByToken возвращает ученика по токену устройства.
func (s *Store) StudentByToken(ctx context.Context, q pg.Querier, hash []byte) (Student, error) {
	st, err := scanStudent(q.QueryRow(ctx, `
		SELECT s.id, s.org_id, s.display_name, s.public_tag, s.public_code,
		       COALESCE(s.level, ''), s.created_at, s.last_seen_at
		FROM iam.student_tokens t
		JOIN iam.students s ON s.id = t.student_id
		WHERE t.token_hash = $1 AND t.expires_at > now()`, hash))
	if pg.IsNoRows(err) {
		return Student{}, ErrNotFound
	}
	if err != nil {
		return Student{}, fmt.Errorf("iam: не удалось прочитать ученика по токену: %w", err)
	}
	return st, nil
}

// DeleteStudentToken выходит только с одного устройства: остальные продолжают
// работать (NR-14).
func (s *Store) DeleteStudentToken(ctx context.Context, q pg.Querier, hash []byte) error {
	_, err := q.Exec(ctx, `DELETE FROM iam.student_tokens WHERE token_hash = $1`, hash)
	return err
}

// DeleteStudentTokens выходит со всех устройств ученика сразу: нужен при
// смене личного кода.
func (s *Store) DeleteStudentTokens(ctx context.Context, q pg.Querier, orgID, studentID uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM iam.student_tokens WHERE student_id = $1 AND org_id = $2`,
		studentID, orgID)
	if err != nil {
		return fmt.Errorf("iam: не удалось закрыть устройства ученика: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Матрица прав
// ---------------------------------------------------------------------------

// LoadRolePermissions читает матрицу ролей из базы.
//
// Загружается один раз при старте: матрица меняется миграцией, а не в рантайме,
// поэтому кешировать её безопасно, а ходить в базу на каждый запрос — нет.
func (s *Store) LoadRolePermissions(ctx context.Context, q pg.Querier) (map[string]map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT role_code, permission_code FROM iam.role_permissions`)
	if err != nil {
		return nil, fmt.Errorf("iam: не удалось прочитать матрицу прав: %w", err)
	}
	defer rows.Close()

	out := map[string]map[string]bool{}
	for rows.Next() {
		var role, perm string
		if err := rows.Scan(&role, &perm); err != nil {
			return nil, fmt.Errorf("iam: не удалось разобрать матрицу прав: %w", err)
		}
		if out[role] == nil {
			out[role] = map[string]bool{}
		}
		out[role][perm] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("iam: матрица прав пуста — не применены миграции?")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Вспомогательное
// ---------------------------------------------------------------------------

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// Адрес клиента здесь не разбирается намеренно. Он приходит параметром из
// обработчика, который берёт его через httpx.ClientIP: тот проверяет, что
// заголовкам X-Forwarded-For можно верить. Разбор RemoteAddr «на месте»
// открывал бы обход счётчика попыток подделкой заголовка.
