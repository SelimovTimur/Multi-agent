package iam

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
)

// SMTPMailer отправляет письма через обычный SMTP.
//
// Обычный SMTP, а не API конкретного провайдера, намеренно: так работает и
// Яндекс 360, и Mail.ru, и почтовый сервер школы — меняются переменные
// окружения, а не код.
//
// Письмо содержит и текстовую, и HTML-часть. Текстовая обязательна: часть
// школьных почтовых клиентов режет HTML, и человек должен увидеть ссылку
// в любом случае.
type SMTPMailer struct {
	cfg       config.MailConfig
	publicURL string

	// tlsConfig подменяется только в тестах: там почтовый сервер поднимается
	// локально с самоподписанным сертификатом. В рабочем коде поле пустое,
	// и сертификат проверяется как положено.
	tlsConfig *tls.Config
}

func NewSMTPMailer(cfg config.MailConfig, publicURL string) *SMTPMailer {
	return &SMTPMailer{cfg: cfg, publicURL: strings.TrimRight(publicURL, "/")}
}

func (m *SMTPMailer) tls() *tls.Config {
	if m.tlsConfig != nil {
		return m.tlsConfig
	}
	return &tls.Config{ServerName: m.cfg.Host}
}

// SendVerification отправляет ссылку подтверждения почты.
func (m *SMTPMailer) SendVerification(ctx context.Context, email, name, token string) error {
	link := m.link("/verify", token)
	return m.send(ctx, email, "Подтвердите почту — На родном",
		verificationText(name, link), verificationHTML(name, link))
}

// SendPasswordReset отправляет ссылку смены пароля.
func (m *SMTPMailer) SendPasswordReset(ctx context.Context, email, name, token string) error {
	link := m.link("/password-reset", token)
	return m.send(ctx, email, "Смена пароля — На родном",
		resetText(name, link), resetHTML(name, link))
}

// SendApprovalRequest отправляет заявку тому, кто её рассматривает.
//
// В письме есть всё, что нужно для решения — имя, адрес, организация, — чтобы
// не пришлось лезть в базу: письмо читают с телефона.
func (m *SMTPMailer) SendApprovalRequest(ctx context.Context, to string, app Applicant, token string) error {
	link := m.link("/approve", token)
	return m.send(ctx, to, "Заявка на доступ — На родном",
		approvalRequestText(app, link), approvalRequestHTML(app, link))
}

// SendAccountApproved зовёт учителя войти.
func (m *SMTPMailer) SendAccountApproved(ctx context.Context, email, name string) error {
	link := m.publicURL + "/login"
	return m.send(ctx, email, "Аккаунт одобрен — На родном",
		approvedText(name, link), approvedHTML(name, link))
}

// SendAccountRejected сообщает об отказе.
func (m *SMTPMailer) SendAccountRejected(ctx context.Context, email, name string) error {
	return m.send(ctx, email, "Заявка отклонена — На родном",
		rejectedText(name), rejectedHTML(name))
}

// link собирает адрес перехода.
//
// Адрес берётся из PUBLIC_URL, а не из заголовка Host входящего запроса:
// иначе кто угодно прислал бы себе письмо со ссылкой на подставной домен и
// собирал бы там чужие токены.
func (m *SMTPMailer) link(path, token string) string {
	return fmt.Sprintf("%s%s?token=%s", m.publicURL, path, url.QueryEscape(token))
}

func (m *SMTPMailer) send(ctx context.Context, to, subject, text, html string) error {
	msg := buildMessage(m.cfg, to, subject, text, html)

	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))
	dialer := &net.Dialer{Timeout: m.cfg.Timeout}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("iam: не удалось соединиться с почтовым сервером %s: %w", addr, err)
	}
	// Дедлайн на всё соединение: залипший почтовый сервер не должен держать
	// горутину, пока её не оборвёт таймаут запроса.
	_ = conn.SetDeadline(time.Now().Add(m.cfg.Timeout))

	// Порт 465 — TLS с первого байта, остальные — STARTTLS после приветствия.
	if m.cfg.Port == 465 {
		conn = tls.Client(conn, m.tls())
	}

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("iam: почтовый сервер не ответил: %w", err)
	}
	defer client.Close()

	if m.cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(m.tls()); err != nil {
				return fmt.Errorf("iam: не удалось поднять TLS: %w", err)
			}
		} else {
			// Пароль от почты по открытому каналу не отправляем ни при каких
			// условиях: лучше не отправить письмо, чем отдать учётку.
			return fmt.Errorf("iam: почтовый сервер %s не поддерживает STARTTLS", m.cfg.Host)
		}
	}

	if m.cfg.Username != "" {
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("iam: почтовый сервер отверг учётные данные: %w", err)
		}
	}

	if err := client.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("iam: отправитель отклонён: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("iam: получатель отклонён: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("iam: сервер не принял тело письма: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		w.Close()
		return fmt.Errorf("iam: не удалось записать письмо: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("iam: сервер не принял письмо: %w", err)
	}

	if err := client.Quit(); err != nil {
		// Письмо уже принято сервером — ошибка на Quit ничего не отменяет.
		logx.From(ctx).Warn("почтовый сервер не закрыл сессию штатно", "err", err)
	}
	return nil
}

// buildMessage собирает MIME-письмо из двух частей.
func buildMessage(cfg config.MailConfig, to, subject, text, html string) []byte {
	// Граница фиксированная: содержимое писем мы формируем сами, и совпасть
	// с ней текст урока не может — он в письма не попадает.
	const boundary = "nr-boundary-8f2c1d"

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", mime.QEncoding.Encode("utf-8", cfg.FromName), cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(text)
	b.WriteString("\r\n\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(html)
	b.WriteString("\r\n\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// Тексты писем
// ---------------------------------------------------------------------------
//
// Письмо пишется учителю, а не разработчику: одна мысль, одна кнопка,
// никаких «вы получили это письмо, потому что...». Срок действия ссылки
// указан явно — иначе человек возвращается к письму через неделю и не
// понимает, почему оно перестало работать.

func verificationText(name, link string) string {
	return fmt.Sprintf(`%s, здравствуйте!

Вы зарегистрировались в сервисе «На родном». Чтобы начать работать,
подтвердите почту — перейдите по ссылке:

%s

Ссылка действует сутки.

Если вы не регистрировались, просто удалите это письмо: без перехода
по ссылке аккаунт останется неактивным.`, greeting(name), link)
}

func verificationHTML(name, link string) string {
	return fmt.Sprintf(`<div style="font-family:system-ui,-apple-system,sans-serif;font-size:15px;line-height:1.6;color:#1F2933">
<p>%s, здравствуйте!</p>
<p>Вы зарегистрировались в сервисе «На родном». Чтобы начать работать, подтвердите почту.</p>
<p style="margin:28px 0">
  <a href="%s" style="background:#1F7A4D;color:#fff;text-decoration:none;padding:12px 22px;border-radius:4px;font-weight:600;display:inline-block">Подтвердить почту</a>
</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Ссылка действует сутки. Если кнопка не работает, скопируйте адрес:<br>%s</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Если вы не регистрировались, просто удалите это письмо — без перехода по ссылке аккаунт останется неактивным.</p>
</div>`, htmlEscape(greeting(name)), htmlEscape(link), htmlEscape(link))
}

func resetText(name, link string) string {
	return fmt.Sprintf(`%s, здравствуйте!

Кто-то запросил смену пароля для вашего аккаунта в сервисе «На родном».
Если это вы, перейдите по ссылке и задайте новый пароль:

%s

Ссылка действует час.

Если вы не запрашивали смену, ничего делать не нужно — пароль останется
прежним.`, greeting(name), link)
}

func resetHTML(name, link string) string {
	return fmt.Sprintf(`<div style="font-family:system-ui,-apple-system,sans-serif;font-size:15px;line-height:1.6;color:#1F2933">
<p>%s, здравствуйте!</p>
<p>Кто-то запросил смену пароля для вашего аккаунта в сервисе «На родном».</p>
<p style="margin:28px 0">
  <a href="%s" style="background:#1F7A4D;color:#fff;text-decoration:none;padding:12px 22px;border-radius:4px;font-weight:600;display:inline-block">Задать новый пароль</a>
</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Ссылка действует час. Если кнопка не работает, скопируйте адрес:<br>%s</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Если вы не запрашивали смену, ничего делать не нужно — пароль останется прежним.</p>
</div>`, htmlEscape(greeting(name)), htmlEscape(link), htmlEscape(link))
}

// Заявка на доступ. Читает её не учитель, а тот, кто решает, — поэтому здесь
// не «здравствуйте», а сразу данные для решения. Две кнопки в одном письме
// не делаем: решение принимается на странице, где видно всю заявку целиком,
// и случайный клик в почте ничего не решает.

func approvalRequestText(app Applicant, link string) string {
	return fmt.Sprintf(`Новая заявка на доступ к сервису «На родном».

Имя: %s
Почта: %s
Организация: %s

Почта подтверждена. Открыть заявку и принять решение:

%s

Пока решения нет, войти человек не может.`,
		greeting(app.Name), app.Email, app.Org, link)
}

func approvalRequestHTML(app Applicant, link string) string {
	return fmt.Sprintf(`<div style="font-family:system-ui,-apple-system,sans-serif;font-size:15px;line-height:1.6;color:#1F2933">
<p>Новая заявка на доступ к сервису «На родном».</p>
<table style="border-collapse:collapse;font-size:15px">
  <tr><td style="padding:2px 16px 2px 0;color:rgba(31,41,51,.6)">Имя</td><td>%s</td></tr>
  <tr><td style="padding:2px 16px 2px 0;color:rgba(31,41,51,.6)">Почта</td><td>%s</td></tr>
  <tr><td style="padding:2px 16px 2px 0;color:rgba(31,41,51,.6)">Организация</td><td>%s</td></tr>
</table>
<p style="margin:28px 0">
  <a href="%s" style="background:#1F7A4D;color:#fff;text-decoration:none;padding:12px 22px;border-radius:4px;font-weight:600;display:inline-block">Открыть заявку</a>
</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Если кнопка не работает, скопируйте адрес:<br>%s</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Пока решения нет, войти человек не может.</p>
</div>`,
		htmlEscape(greeting(app.Name)), htmlEscape(app.Email), htmlEscape(app.Org),
		htmlEscape(link), htmlEscape(link))
}

func approvedText(name, link string) string {
	return fmt.Sprintf(`%s, здравствуйте!

Ваш аккаунт в сервисе «На родном» одобрен — можно войти:

%s

Почта и пароль те же, что при регистрации.`, greeting(name), link)
}

func approvedHTML(name, link string) string {
	return fmt.Sprintf(`<div style="font-family:system-ui,-apple-system,sans-serif;font-size:15px;line-height:1.6;color:#1F2933">
<p>%s, здравствуйте!</p>
<p>Ваш аккаунт в сервисе «На родном» одобрен — можно войти.</p>
<p style="margin:28px 0">
  <a href="%s" style="background:#1F7A4D;color:#fff;text-decoration:none;padding:12px 22px;border-radius:4px;font-weight:600;display:inline-block">Войти</a>
</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Почта и пароль те же, что при регистрации. Если кнопка не работает, скопируйте адрес:<br>%s</p>
</div>`, htmlEscape(greeting(name)), htmlEscape(link), htmlEscape(link))
}

// Отказ. Причину не пишем: её у нас нет в машинном виде, а придуманная
// формулировка хуже честного «напишите нам».
func rejectedText(name string) string {
	return fmt.Sprintf(`%s, здравствуйте!

Мы рассмотрели заявку на доступ к сервису «На родном» и не можем её
подтвердить.

Если это ошибка, ответьте на это письмо — разберёмся.`, greeting(name))
}

func rejectedHTML(name string) string {
	return fmt.Sprintf(`<div style="font-family:system-ui,-apple-system,sans-serif;font-size:15px;line-height:1.6;color:#1F2933">
<p>%s, здравствуйте!</p>
<p>Мы рассмотрели заявку на доступ к сервису «На родном» и не можем её подтвердить.</p>
<p style="color:rgba(31,41,51,.6);font-size:13px">Если это ошибка, ответьте на это письмо — разберёмся.</p>
</div>`, htmlEscape(greeting(name)))
}

// greeting подставляет имя, если оно есть.
func greeting(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Здравствуйте"
	}
	return name
}

// htmlEscape закрывает угловые скобки и кавычки.
//
// Имя пользователь вводит сам, и без экранирования оно уезжает в письмо
// как разметка.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
