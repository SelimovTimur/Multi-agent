package iam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Mailer отправляет письма подтверждения и восстановления.
//
// Интерфейс, а не конкретный провайдер: почтового провайдера на пилоте ещё
// нет (блокер B3), и до его появления работает LogMailer, который печатает
// ссылку в лог. Подключение реального провайдера не трогает сервис.
type Mailer interface {
	SendVerification(ctx context.Context, email, displayName, token string) error
	SendPasswordReset(ctx context.Context, email, displayName, token string) error
	// SendApprovalRequest уходит не учителю, а тому, кто рассматривает заявку.
	SendApprovalRequest(ctx context.Context, to string, applicant Applicant, token string) error
	SendAccountApproved(ctx context.Context, email, displayName string) error
	SendAccountRejected(ctx context.Context, email, displayName string) error
}

// Applicant — заявитель в письме рассматривающему.
//
// Отдельный тип, а не четыре строковых аргумента: у SendApprovalRequest иначе
// получается шесть параметров подряд, и перепутать местами имя с организацией
// становится вопросом времени.
type Applicant struct {
	Name  string
	Email string
	Org   string
}

// LogMailer печатает ссылку в лог вместо отправки письма.
//
// Только для разработки. Печатается именно ссылка, а не токен: по ссылке
// можно кликнуть и пройти сценарий целиком, а токен пришлось бы вручную
// вставлять в адресную строку.
//
// На проде такой почтальон означал бы, что зарегистрироваться может только
// тот, у кого есть доступ к логам. Поэтому прод с ним не стартует —
// см. internal/app.
type LogMailer struct {
	// PublicURL — адрес сервиса снаружи. Пустой даёт ссылки без домена,
	// в логе это всё равно читаемо.
	PublicURL string
}

func (m LogMailer) SendVerification(ctx context.Context, email, name, token string) error {
	logx.From(ctx).Info("ПОЧТА НЕ НАСТРОЕНА: ссылка подтверждения только в логе",
		"email", email, "link", m.link("/verify", token))
	return nil
}

func (m LogMailer) SendPasswordReset(ctx context.Context, email, name, token string) error {
	logx.From(ctx).Info("ПОЧТА НЕ НАСТРОЕНА: ссылка смены пароля только в логе",
		"email", email, "link", m.link("/password-reset", token))
	return nil
}

func (m LogMailer) SendApprovalRequest(ctx context.Context, to string, app Applicant, token string) error {
	logx.From(ctx).Info("ПОЧТА НЕ НАСТРОЕНА: заявка на одобрение только в логе",
		"approver", to, "applicant", app.Email, "org", app.Org,
		"link", m.link("/approve", token))
	return nil
}

func (m LogMailer) SendAccountApproved(ctx context.Context, email, name string) error {
	logx.From(ctx).Info("ПОЧТА НЕ НАСТРОЕНА: письмо об одобрении только в логе", "email", email)
	return nil
}

func (m LogMailer) SendAccountRejected(ctx context.Context, email, name string) error {
	logx.From(ctx).Info("ПОЧТА НЕ НАСТРОЕНА: письмо об отказе только в логе", "email", email)
	return nil
}

func (m LogMailer) link(path, token string) string {
	return strings.TrimRight(m.PublicURL, "/") + path + "?token=" + url.QueryEscape(token)
}

// VerificationLink делает LogMailer'а DevLinkMailer'ом.
func (m LogMailer) VerificationLink(token string) string { return m.link("/verify", token) }

// ApprovalLink — ссылка одобрения для того же показа без почты: иначе на
// стенде заявку некому увидеть, и сценарий не пройти до конца.
func (m LogMailer) ApprovalLink(token string) string { return m.link("/approve", token) }

// DevLinkMailer — почтальон, который писем не отправляет и потому может
// показать ссылку подтверждения прямо в ответе API.
//
// Интерфейс реализует только LogMailer. Это и есть защита: настоящий
// SMTPMailer его не реализует, а прод без SMTP не стартует — значит на
// проде ссылка в ответ не попадёт никак.
//
// Нужен для показа и локальной разработки: сценарий регистрации проходится
// целиком, с настоящим одноразовым токеном, без доступа к логам сервера.
type DevLinkMailer interface {
	Mailer
	VerificationLink(token string) string
	ApprovalLink(token string) string
}

// Service — прикладная логика аккаунтов и учеников.
type Service struct {
	pool  *pg.Pool
	store *Store
	cfg   config.AuthConfig
	mail  Mailer
	perms map[string]map[string]bool
}

// NewService собирает сервис и загружает матрицу прав.
//
// Матрица читается один раз при старте: она меняется миграцией, а не в
// рантайме. Пустая матрица — отказ старта, а не тихая работа без прав.
func NewService(ctx context.Context, pool *pg.Pool, cfg config.AuthConfig, mail Mailer) (*Service, error) {
	store := NewStore()
	perms, err := store.LoadRolePermissions(ctx, pool)
	if err != nil {
		return nil, err
	}
	if mail == nil {
		mail = LogMailer{}
	}
	return &Service{pool: pool, store: store, cfg: cfg, mail: mail, perms: perms}, nil
}

// Can проверяет право роли по матрице.
func (s *Service) Can(role, permission string) bool {
	return s.perms[role][permission]
}

// Permissions возвращает права роли — фронт по ним прячет недоступные действия.
func (s *Service) Permissions(role string) []string {
	out := make([]string, 0, len(s.perms[role]))
	for p := range s.perms[role] {
		out = append(out, p)
	}
	// Порядок обхода map случайный. Сортируем, чтобы ответ API был
	// стабильным: иначе тесты фронта и снапшоты падают через раз.
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Валидация
// ---------------------------------------------------------------------------

// Простая проверка формы адреса. Полная валидация email по RFC бессмысленна:
// единственная надёжная проверка — письмо со ссылкой, оно и делается.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

const minPasswordLen = 8

func validateRegistration(email, password, name string) error {
	fields := map[string]string{}
	if !emailRe.MatchString(NormalizeEmail(email)) {
		fields["email"] = "Похоже, адрес указан неверно"
	}
	if len([]rune(password)) < minPasswordLen {
		fields["password"] = fmt.Sprintf("Не короче %d символов", minPasswordLen)
	}
	if strings.TrimSpace(name) == "" {
		fields["name"] = "Укажите имя"
	}
	if len(fields) > 0 {
		return httpx.Validation("Проверьте заполнение формы", fields)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Регистрация и подтверждение почты
// ---------------------------------------------------------------------------

// msgDeadLink — единый ответ на любую нерабочую ссылку из письма.
//
// Один текст на «не существует», «просрочена» и «уже использована»: разные
// ответы позволяли бы перебором выяснять, какие токены когда-то существовали.
const msgDeadLink = "Ссылка недействительна или уже использована"

// RegisterResult — что вернуть после регистрации.
type RegisterResult struct {
	User User
	Org  Organization

	// DevVerifyURL заполняется, только когда почта не настроена: тогда
	// ссылку показывает сам экран «Проверьте почту». Пустая строка в любом
	// окружении, где письма действительно отправляются.
	DevVerifyURL string
}

// Register создаёт организацию и учителя одной транзакцией.
//
// Организация создаётся всегда, даже для одиночного репетитора (kind=b2c):
// одна модель для школы и репетитора избавляет от ветвления во всех
// последующих запросах, а тенант появляется сразу, а не после «апгрейда».
//
// Аккаунт создаётся неактивным: между регистрацией и входом стоят два шага —
// подтверждение почты и одобрение заявки человеком.
// Занятый адрес отдаёт 409 без раскрытия статуса существующего аккаунта.
func (s *Service) Register(ctx context.Context, email, password, name string) (RegisterResult, error) {
	if err := validateRegistration(email, password, name); err != nil {
		return RegisterResult{}, err
	}

	hash, err := HashPassword(password, s.argon())
	if err != nil {
		return RegisterResult{}, httpx.Internal("Не удалось создать аккаунт").Wrap(err)
	}

	name = strings.TrimSpace(name)
	org := Organization{
		ID:     idgen.New(),
		Kind:   OrgKindB2C,
		Title:  name,
		Status: "active",
	}
	user := User{
		ID:           idgen.New(),
		OrgID:        org.ID,
		Email:        strings.TrimSpace(email),
		DisplayName:  name,
		Role:         RoleTeacher,
		Status:       StatusPending,
		Origin:       OriginSelf,
		PasswordHash: hash,
	}

	rawToken, tokenHash, err := NewToken()
	if err != nil {
		return RegisterResult{}, httpx.Internal("Не удалось создать аккаунт").Wrap(err)
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.store.CreateOrg(ctx, tx, org); err != nil {
			return err
		}
		if err := s.store.CreateUser(ctx, tx, user); err != nil {
			if pg.IsUniqueViolation(err) {
				return httpx.Conflict("Этот адрес уже занят")
			}
			return err
		}
		return s.store.CreateEmailToken(ctx, tx, idgen.New(), user.OrgID, user.ID, tokenKindVerify,
			tokenHash, time.Now().Add(s.cfg.VerifyTokenTTL))
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return RegisterResult{}, err
		}
		return RegisterResult{}, httpx.Internal("Не удалось создать аккаунт").Wrap(err)
	}

	// Письмо отправляем после коммита: иначе при откате транзакции у человека
	// останется рабочая ссылка на несуществующий аккаунт.
	if err := s.mail.SendVerification(ctx, user.Email, user.DisplayName, rawToken); err != nil {
		logx.From(ctx).Error("не удалось отправить письмо подтверждения",
			"user_id", user.ID, "err", err)
	}

	res := RegisterResult{User: user, Org: org}
	if dm, ok := s.mail.(DevLinkMailer); ok {
		res.DevVerifyURL = dm.VerificationLink(rawToken)
	}
	return res, nil
}

// VerifyResult — что вернуть после перехода по ссылке подтверждения.
type VerifyResult struct {
	User User

	// DevApproveURL заполняется, только когда почта не настроена: заявку
	// иначе никто не увидит, и сценарий на стенде упирается в тупик. Пустая
	// строка везде, где письма действительно уходят.
	DevApproveURL string
}

// VerifyEmail подтверждает почту и ставит заявку в очередь на одобрение.
//
// Сессия здесь НЕ открывается, хотя переход по ссылке и доказывает владение
// адресом: подтверждённая почта больше не даёт права входа, его даёт решение
// человека. Открытая сессия означала бы вход в обход этого решения.
//
// Исключение — адреса из AUTH_AUTO_APPROVE_EMAILS: это ящики самой команды,
// и ждать одобрения от самих себя незачем.
func (s *Service) VerifyEmail(ctx context.Context, rawToken string) (VerifyResult, error) {
	var user User
	var org Organization
	var approveToken string

	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		userID, err := s.store.ConsumeEmailToken(ctx, tx, HashToken(rawToken), tokenKindVerify)
		if errors.Is(err, ErrNotFound) {
			return httpx.NotFound(msgDeadLink)
		}
		if err != nil {
			return err
		}
		if user, err = s.store.UserByID(ctx, tx, userID); err != nil {
			return err
		}

		if s.autoApproved(user.Email) {
			if _, err := s.store.ActivateUser(ctx, tx, userID); err != nil {
				return err
			}
		} else {
			moved, err := s.store.ConfirmEmail(ctx, tx, userID)
			if err != nil {
				return err
			}
			// Заявку заводим только на тех, кто действительно перешёл в
			// ожидание. Заблокированный аккаунт по старой ссылке заявку не
			// создаёт: это была бы просьба разблокировать, замаскированная
			// под регистрацию.
			if moved {
				raw, hash, err := NewToken()
				if err != nil {
					return err
				}
				approveToken = raw
				if err := s.store.CreateEmailToken(ctx, tx, idgen.New(), user.OrgID, user.ID,
					tokenKindApprove, hash, time.Now().Add(s.cfg.ApproveTokenTTL)); err != nil {
					return err
				}
				if org, err = s.store.OrgByID(ctx, tx, user.OrgID); err != nil {
					return err
				}
			}
		}

		// Перечитываем: статус только что изменился, а фронту нужен текущий.
		user, err = s.store.UserByID(ctx, tx, userID)
		return err
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return VerifyResult{}, err
		}
		return VerifyResult{}, httpx.Internal("Не удалось подтвердить почту").Wrap(err)
	}

	res := VerifyResult{User: user}
	if approveToken != "" {
		// Письма шлём после коммита: при откате транзакции рассматривать было
		// бы нечего, а ссылка в почте уже лежала бы.
		s.sendApprovalRequests(ctx, Applicant{
			Name:  user.DisplayName,
			Email: user.Email,
			Org:   org.Title,
		}, approveToken)
		if dm, ok := s.mail.(DevLinkMailer); ok {
			res.DevApproveURL = dm.ApprovalLink(approveToken)
		}
	}
	return res, nil
}

// autoApproved — адрес из списка самоодобрения.
//
// Сравнение идёт по нормализованному адресу: в переменной окружения его
// напишут как угодно, а в базе он лежит в нижнем регистре.
func (s *Service) autoApproved(email string) bool {
	norm := NormalizeEmail(email)
	if norm == "" {
		return false
	}
	for _, allowed := range s.cfg.AutoApproveEmails {
		if NormalizeEmail(allowed) == norm {
			return true
		}
	}
	return false
}

// sendApprovalRequests рассылает заявку рассматривающим.
//
// Ошибка отправки не роняет подтверждение почты: заявка уже в базе, и письмо
// можно перевыслать. Пустой список — предупреждение в лог, а не тихая
// потеря: молча повисшая заявка выглядит как «регистрация не работает».
func (s *Service) sendApprovalRequests(ctx context.Context, app Applicant, token string) {
	if len(s.cfg.ApproverEmails) == 0 {
		logx.From(ctx).Warn("заявку на одобрение некому отправить: задайте AUTH_APPROVER_EMAILS или SMTP_FROM",
			"applicant", app.Email)
		return
	}
	for _, to := range s.cfg.ApproverEmails {
		if err := s.mail.SendApprovalRequest(ctx, to, app, token); err != nil {
			logx.From(ctx).Error("не удалось отправить заявку на одобрение",
				"approver", to, "err", err)
		}
	}
}

// ResendVerification повторно отправляет письмо подтверждения.
//
// Ответ одинаков независимо от того, существует ли аккаунт и не сработал ли
// лимит: иначе форма превращается в проверялку зарегистрированных адресов —
// по одному только «слишком часто» видно, что адрес зарегистрирован.
func (s *Service) ResendVerification(ctx context.Context, email string) error {
	user, err := s.store.UserByEmail(ctx, s.pool, email)
	// Подтверждать нечего всем, кроме тех, кто ещё не подтвердил почту.
	// awaiting_approval сюда не входит: почта у них уже подтверждена, ждут они
	// решения человека, и повторное письмо ничего не ускорит.
	if errors.Is(err, ErrNotFound) || (err == nil && user.Status != StatusPending) {
		return nil
	}
	if err != nil {
		return httpx.Internal("Не удалось отправить письмо").Wrap(err)
	}

	rawToken, tokenHash, err := NewToken()
	if err != nil {
		return httpx.Internal("Не удалось отправить письмо").Wrap(err)
	}

	sent := false
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		ok, err := s.mayMail(ctx, tx, user.ID, tokenKindVerify)
		if err != nil || !ok {
			return err
		}
		if err := s.store.InvalidateEmailTokens(ctx, tx, user.ID, tokenKindVerify); err != nil {
			return err
		}
		if err := s.store.CreateEmailToken(ctx, tx, idgen.New(), user.OrgID, user.ID, tokenKindVerify,
			tokenHash, time.Now().Add(s.cfg.VerifyTokenTTL)); err != nil {
			return err
		}
		sent = true
		return nil
	})
	if err != nil {
		return httpx.Internal("Не удалось отправить письмо").Wrap(err)
	}
	if !sent {
		return nil
	}
	if err := s.mail.SendVerification(ctx, user.Email, user.DisplayName, rawToken); err != nil {
		logx.From(ctx).Error("не удалось отправить письмо подтверждения", "err", err)
	}
	return nil
}

// mailInterval — минимальный промежуток между письмами одного вида одному
// адресу (NR-7).
//
// Ограничение защищает не нас, а владельца ящика: без него форма «выслать
// ещё раз» превращается в бесплатную рассылку по чужому адресу.
const mailInterval = time.Minute

// mayMail решает, можно ли отправлять письмо этого вида прямо сейчас.
//
// Считает по времени последнего выданного токена — оно и есть время
// последнего письма. Строка пользователя блокируется на время проверки:
// реплик API две, и без замка обе прочитают одно и то же «давно» и отправят
// по письму. Счётчик живёт в базе, а не в памяти процесса, по той же
// причине — и чтобы переживать перезапуск.
func (s *Service) mayMail(ctx context.Context, tx pgx.Tx, userID uuid.UUID, kind string) (bool, error) {
	if err := s.store.LockUser(ctx, tx, userID); err != nil {
		return false, err
	}
	last, ok, err := s.store.LastEmailTokenAt(ctx, tx, userID, kind)
	if err != nil {
		return false, err
	}
	if tooSoon(last, ok, time.Now()) {
		logx.From(ctx).Info("письмо не отправлено: слишком часто", "user_id", userID, "kind", kind)
		return false, nil
	}
	return true, nil
}

// tooSoon — само правило лимита, отдельно от похода в базу: так его видно и
// можно проверить тестом. sent=false означает «писем ещё не было».
func tooSoon(last time.Time, sent bool, now time.Time) bool {
	return sent && now.Sub(last) < mailInterval
}

// ---------------------------------------------------------------------------
// Одобрение заявок
//
// Административного экрана нет намеренно: на пилоте заявок единицы в неделю,
// и кнопка в письме дешевле целого раздела кабинета. Авторизует действие сам
// токен из письма — вошедшего пользователя здесь нет.
// ---------------------------------------------------------------------------

// Approval читает заявку по ссылке из письма, НЕ принимая решения.
//
// Отдельно от решения, потому что GET не имеет права менять состояние:
// почтовые антивирусы и превьюшки мессенджеров ходят по ссылкам сами и
// одобрили бы заявку раньше человека.
func (s *Service) Approval(ctx context.Context, rawToken string) (ApprovalRequest, error) {
	if strings.TrimSpace(rawToken) == "" {
		return ApprovalRequest{}, httpx.NotFound(msgDeadLink)
	}
	req, err := s.store.ApprovalByTokenHash(ctx, s.pool, HashToken(rawToken))
	if errors.Is(err, ErrNotFound) {
		return ApprovalRequest{}, httpx.NotFound(msgDeadLink)
	}
	if err != nil {
		return ApprovalRequest{}, httpx.Internal("Не удалось прочитать заявку").Wrap(err)
	}
	return req, nil
}

// DecideApproval применяет решение и гасит ссылку.
//
// Токен одноразовый: повторный переход по той же ссылке отвечает так же, как
// по несуществующей, — решение уже принято, и менять его ссылкой из письма
// нельзя.
func (s *Service) DecideApproval(ctx context.Context, rawToken, decision string) (ApprovalRequest, error) {
	if decision != DecisionApprove && decision != DecisionReject {
		return ApprovalRequest{}, httpx.FieldError("decision", "Допустимо «approve» или «reject»")
	}
	if strings.TrimSpace(rawToken) == "" {
		return ApprovalRequest{}, httpx.NotFound(msgDeadLink)
	}

	var req ApprovalRequest
	var changed bool
	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if req, err = s.store.ApprovalByTokenHash(ctx, tx, HashToken(rawToken)); err != nil {
			if errors.Is(err, ErrNotFound) {
				return httpx.NotFound(msgDeadLink)
			}
			return err
		}
		if _, err = s.store.ConsumeEmailToken(ctx, tx, HashToken(rawToken), tokenKindApprove); err != nil {
			if errors.Is(err, ErrNotFound) {
				return httpx.NotFound(msgDeadLink)
			}
			return err
		}

		if decision == DecisionApprove {
			changed, err = s.store.ActivateUser(ctx, tx, req.UserID)
		} else {
			changed, err = s.store.RejectUser(ctx, tx, req.UserID)
		}
		if err != nil {
			return err
		}

		user, err := s.store.UserByID(ctx, tx, req.UserID)
		if err != nil {
			return err
		}
		req.Status = user.Status
		req.Decided = true
		return nil
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return ApprovalRequest{}, err
		}
		return ApprovalRequest{}, httpx.Internal("Не удалось применить решение").Wrap(err)
	}

	// Письмо уходит только когда статус действительно изменился: если учётку
	// успели заблокировать другим путём, «аккаунт одобрен» было бы враньём.
	if changed {
		s.notifyDecision(ctx, req, decision)
	}
	return req, nil
}

func (s *Service) notifyDecision(ctx context.Context, req ApprovalRequest, decision string) {
	var err error
	if decision == DecisionApprove {
		err = s.mail.SendAccountApproved(ctx, req.Email, req.Name)
	} else {
		err = s.mail.SendAccountRejected(ctx, req.Email, req.Name)
	}
	if err != nil {
		// Решение уже применено — падать поздно и незачем. Человек узнает о
		// нём при первой попытке входа, а мы увидим ошибку в логе.
		logx.From(ctx).Error("не удалось отправить письмо о решении по заявке",
			"user_id", req.UserID, "decision", decision, "err", err)
	}
}

// ---------------------------------------------------------------------------
// Вход и сессии
// ---------------------------------------------------------------------------

// LoginResult — сессия и её открытый токен. Токен существует только здесь:
// в базе лежит хеш.
type LoginResult struct {
	User    User
	Session Session
	Token   string
}

// errLoginRefused — единый ответ на неверный пароль и несуществующий адрес.
//
// Оба случая отвечают ОДИНАКОВО: иначе форма входа рассказывает, какие
// адреса зарегистрированы.
func errLoginRefused() error {
	return httpx.Unauthorized("Неверная почта или пароль")
}

// errStatusRefused объясняет, почему верный пароль не открывает вход.
//
// Здесь мы уже знаем, что пароль верный, — значит перед нами хозяин аккаунта,
// и скрывать от него состояние его же учётки незачем. Скрывали бы — человек
// молча упирался бы в «неверный пароль», менял бы пароль по кругу и писал в
// поддержку. Для чужого адреса этот ответ недостижим: без пароля до него не
// дойти.
//
// nil означает «статус входу не мешает».
//
// Статус дублируется в fields: поле задумано под ошибки валидации, но это
// единственный машиночитаемый канал в формате ошибки (httpx.Error), а фронту
// нужно развести экраны «подтвердите почту» и «заявка на рассмотрении», не
// сверяя текст сообщения.
func errStatusRefused(status string) error {
	msg := ""
	switch status {
	case StatusActive:
		return nil
	case StatusPending:
		msg = "Подтвердите почту — мы отправили ссылку на неё"
	case StatusAwaitingApproval:
		msg = "Заявка на рассмотрении. Мы напишем, когда аккаунт одобрят"
	default:
		// blocked, deleted и всё, что появится позже: подробности закрытого
		// доступа человеку не помогут, а нам обещать нечего.
		msg = "Доступ закрыт"
	}
	e := httpx.Forbidden(msg)
	e.Fields = map[string]string{"status": status}
	return e
}

// Login проверяет пару и открывает сессию.
func (s *Service) Login(ctx context.Context, email, password, userAgent string, ip *netip.Addr) (LoginResult, error) {
	if strings.TrimSpace(email) == "" || password == "" {
		return LoginResult{}, errLoginRefused()
	}

	// Защита от подбора: считается по паре (email, IP). Это не продуктовая
	// квота — продуктовые лимиты идут по org_id и user_id (инвариант И-9).
	since := time.Now().Add(-s.cfg.LoginWindow)
	failed, err := s.store.CountFailedLogins(ctx, s.pool, email, ip, since)
	if err != nil {
		return LoginResult{}, httpx.Internal("Не удалось выполнить вход").Wrap(err)
	}
	if failed >= s.cfg.LoginMaxAttempts {
		return LoginResult{}, httpx.RateLimited(
			fmt.Sprintf("Слишком много попыток. Попробуйте через %d минут",
				int(s.cfg.LoginWindow.Minutes())))
	}

	user, err := s.store.UserByEmail(ctx, s.pool, email)
	switch {
	case errors.Is(err, ErrNotFound):
		s.recordAttempt(ctx, email, ip, false)
		// Считаем хеш даже для несуществующего аккаунта: иначе разница во
		// времени ответа выдаёт, зарегистрирован адрес или нет.
		_ = VerifyPassword(password, dummyHash)
		return LoginResult{}, errLoginRefused()
	case err != nil:
		return LoginResult{}, httpx.Internal("Не удалось выполнить вход").Wrap(err)
	}

	if err := VerifyPassword(password, user.PasswordHash); err != nil {
		s.recordAttempt(ctx, email, ip, false)
		return LoginResult{}, errLoginRefused()
	}
	// Верный пароль на неактивной учётке НЕ считается попыткой подбора:
	// счётчик существует ради чужих паролей, а человек, который знает свой,
	// иначе заблокировал бы себе вход на 15 минут, ожидая одобрения.
	if err := errStatusRefused(user.Status); err != nil {
		return LoginResult{}, err
	}

	res, err := s.openSession(ctx, user, nil, false, userAgent, ip)
	if err != nil {
		return LoginResult{}, err
	}
	s.recordAttempt(ctx, email, ip, true)
	return res, nil
}

// openSession создаёт сессию для пользователя.
// impersonatedBy непустой, когда сессию открывает администратор (NEW-18).
func (s *Service) openSession(ctx context.Context, user User, impersonatedBy *uuid.UUID,
	readOnly bool, userAgent string, ip *netip.Addr) (LoginResult, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return LoginResult{}, httpx.Internal("Не удалось выполнить вход").Wrap(err)
	}
	sess := Session{
		ID:             idgen.New(),
		OrgID:          user.OrgID,
		UserID:         user.ID,
		ImpersonatedBy: impersonatedBy,
		ReadOnly:       readOnly,
		ExpiresAt:      time.Now().Add(s.cfg.SessionTTL),
	}
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.store.CreateSession(ctx, tx, sess, hash, userAgent, ip); err != nil {
			return err
		}
		return s.store.TouchLastLogin(ctx, tx, user.ID)
	})
	if err != nil {
		return LoginResult{}, httpx.Internal("Не удалось выполнить вход").Wrap(err)
	}
	return LoginResult{User: user, Session: sess, Token: raw}, nil
}

func (s *Service) recordAttempt(ctx context.Context, email string, ip *netip.Addr, ok bool) {
	// Журнал попыток не должен ронять вход: он вспомогательный.
	if err := s.store.RecordLoginAttempt(context.WithoutCancel(ctx), s.pool, email, ip, ok); err != nil {
		logx.From(ctx).Warn("не удалось записать попытку входа", "err", err)
	}
}

// dummyHash — валидный argon2id-хеш заведомо неизвестного пароля.
// Нужен, чтобы вход по несуществующему адресу занимал столько же времени,
// сколько вход по существующему.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$" +
	"QQZ6Xk9xCJ0K7pQjT4t3kq0gS6xkE8i2N0oRJ1sYm5g"

// sessionTouchEvery — как часто продлевать сессию учителя. Совпадает с
// порогом в TouchSession.
const sessionTouchEvery = time.Hour

// Authenticate разбирает токен сессии в актора.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (Actor, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Actor{}, httpx.Unauthorized("Нужно войти")
	}
	sess, user, err := s.store.SessionByToken(ctx, s.pool, HashToken(rawToken))
	if errors.Is(err, ErrNotFound) {
		return Actor{}, httpx.Unauthorized("Сессия истекла, войдите заново")
	}
	if err != nil {
		return Actor{}, httpx.Internal("Не удалось проверить сессию").Wrap(err)
	}
	if !user.Active() {
		return Actor{}, httpx.Unauthorized("Доступ к аккаунту закрыт")
	}

	// Продление сессии не должно блокировать запрос и не должно отменяться
	// вместе с ним: пишем в отсоединённом контексте. Продлевается она раз в
	// час (тот же порог в TouchSession), и время прошлого продления уже
	// прочитано — чаще в базу не ходим.
	if time.Since(sess.LastSeenAt) >= sessionTouchEvery {
		go func() {
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := s.store.TouchSession(bg, s.pool, sess.ID, s.cfg.SessionTTL); err != nil {
				logx.From(bg).Warn("не удалось продлить сессию", "err", err)
			}
		}()
	}

	return Actor{
		Kind:         "user",
		UserID:       user.ID,
		OrgID:        user.OrgID,
		Role:         user.Role,
		SessionID:    sess.ID,
		ReadOnly:     sess.ReadOnly,
		Impersonator: sess.ImpersonatedBy,
	}, nil
}

// Logout закрывает текущую сессию.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, s.pool, HashToken(rawToken)); err != nil {
		return httpx.Internal("Не удалось выйти").Wrap(err)
	}
	return nil
}

// UserByID отдаёт пользователя. Нужен обработчику /auth/status и другим модулям.
func (s *Service) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	u, err := s.store.UserByID(ctx, s.pool, id)
	if errors.Is(err, ErrNotFound) {
		return User{}, httpx.NotFound("Пользователь не найден")
	}
	if err != nil {
		return User{}, httpx.Internal("Не удалось прочитать профиль").Wrap(err)
	}
	return u, nil
}

// ActorByEmail собирает актора учителя по почте — для служебных команд вроде
// импорта учеников (cmd/roster), где действие идёт от имени учителя, но без
// его сессии.
//
// Действует только активная учётка: команда не должна заводить классы тому,
// кто сам войти не может.
func (s *Service) ActorByEmail(ctx context.Context, email string) (Actor, error) {
	u, err := s.store.UserByEmail(ctx, s.pool, email)
	if errors.Is(err, ErrNotFound) {
		return Actor{}, httpx.NotFound("Пользователь " + email + " не найден")
	}
	if err != nil {
		return Actor{}, httpx.Internal("Не удалось прочитать пользователя").Wrap(err)
	}
	if !CanLogin(u.Status) {
		return Actor{}, httpx.Conflict("Учётка " + email + " не активна: " + u.Status)
	}
	return Actor{Kind: "user", UserID: u.ID, OrgID: u.OrgID, Role: u.Role}, nil
}

// ---------------------------------------------------------------------------
// Профиль и пароль
// ---------------------------------------------------------------------------

// UpdateProfile меняет отображаемое имя. Email не редактируется: он привязан
// к аккаунту и служит идентификатором входа.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return httpx.FieldError("name", "Укажите имя")
	}
	if err := s.store.UpdateDisplayName(ctx, s.pool, userID, name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return httpx.NotFound("Пользователь не найден")
		}
		return httpx.Internal("Не удалось сохранить профиль").Wrap(err)
	}
	return nil
}

// ChangePassword меняет пароль и завершает остальные сессии.
//
// Текущая сессия сохраняется: иначе смена пароля выкидывает человека из
// формы, в которой он её только что менял.
func (s *Service) ChangePassword(ctx context.Context, actor Actor, current, next string) error {
	if len([]rune(next)) < minPasswordLen {
		return httpx.FieldError("password", fmt.Sprintf("Не короче %d символов", minPasswordLen))
	}
	user, err := s.store.UserByID(ctx, s.pool, actor.UserID)
	if err != nil {
		return httpx.Internal("Не удалось сменить пароль").Wrap(err)
	}
	if err := VerifyPassword(current, user.PasswordHash); err != nil {
		return httpx.FieldError("current", "Текущий пароль указан неверно")
	}
	hash, err := HashPassword(next, s.argon())
	if err != nil {
		return httpx.Internal("Не удалось сменить пароль").Wrap(err)
	}
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.store.UpdatePassword(ctx, tx, actor.UserID, hash); err != nil {
			return err
		}
		return s.store.DeleteUserSessions(ctx, tx, actor.UserID, actor.SessionID)
	})
	if err != nil {
		return httpx.Internal("Не удалось сменить пароль").Wrap(err)
	}
	return nil
}

// RequestPasswordReset отправляет ссылку восстановления.
// Ответ одинаков для любого адреса — существование аккаунта не раскрывается.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	user, err := s.store.UserByEmail(ctx, s.pool, email)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return httpx.Internal("Не удалось отправить письмо").Wrap(err)
	}

	raw, hash, err := NewToken()
	if err != nil {
		return httpx.Internal("Не удалось отправить письмо").Wrap(err)
	}

	sent := false
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Тот же лимит, что и на повторное подтверждение (NR-7): форма
		// восстановления так же открыта всем и так же шлёт письма на чужой
		// адрес. Молчаливый отказ — часть неразличимости ответа.
		ok, err := s.mayMail(ctx, tx, user.ID, tokenKindReset)
		if err != nil || !ok {
			return err
		}
		if err := s.store.InvalidateEmailTokens(ctx, tx, user.ID, tokenKindReset); err != nil {
			return err
		}
		if err := s.store.CreateEmailToken(ctx, tx, idgen.New(), user.OrgID, user.ID, tokenKindReset,
			hash, time.Now().Add(s.cfg.ResetTokenTTL)); err != nil {
			return err
		}
		sent = true
		return nil
	})
	if err != nil {
		return httpx.Internal("Не удалось отправить письмо").Wrap(err)
	}
	if !sent {
		return nil
	}
	if err := s.mail.SendPasswordReset(ctx, user.Email, user.DisplayName, raw); err != nil {
		logx.From(ctx).Error("не удалось отправить письмо восстановления", "err", err)
	}
	return nil
}

// ResetPassword ставит новый пароль по одноразовой ссылке и завершает ВСЕ
// сессии пользователя: если пароль восстанавливали из-за угона, чужая сессия
// не должна пережить смену.
func (s *Service) ResetPassword(ctx context.Context, rawToken, password string) error {
	if len([]rune(password)) < minPasswordLen {
		return httpx.FieldError("password", fmt.Sprintf("Не короче %d символов", minPasswordLen))
	}
	hash, err := HashPassword(password, s.argon())
	if err != nil {
		return httpx.Internal("Не удалось сменить пароль").Wrap(err)
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		userID, err := s.store.ConsumeEmailToken(ctx, tx, HashToken(rawToken), tokenKindReset)
		if errors.Is(err, ErrNotFound) {
			return httpx.NotFound(msgDeadLink)
		}
		if err != nil {
			return err
		}
		if err := s.store.UpdatePassword(ctx, tx, userID, hash); err != nil {
			return err
		}
		// Статус здесь не трогаем. Раньше сброс пароля заодно активировал
		// аккаунт — и это был обход и подтверждения почты, и блокировки:
		// «забыли пароль» возвращал вход тому, кому его закрыли. Владение
		// ящиком доказывает только владение ящиком.
		return s.store.DeleteUserSessions(ctx, tx, userID, uuid.Nil)
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return err
		}
		return httpx.Internal("Не удалось сменить пароль").Wrap(err)
	}
	return nil
}

func (s *Service) argon() ArgonParams {
	return ArgonParams{
		Time:    s.cfg.ArgonTime,
		Memory:  s.cfg.ArgonMemory,
		Threads: s.cfg.ArgonThreads,
		KeyLen:  s.cfg.ArgonKeyLen,
	}
}
