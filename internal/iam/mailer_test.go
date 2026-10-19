package iam

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
)

// Почта — единственный способ активировать аккаунт. Если письмо не уйдёт или
// уйдёт битым, учитель не войдёт вообще, и узнаем мы об этом от него, а не от
// тестов. Поэтому здесь поднимается настоящий SMTP-сервер и проверяется весь
// разговор целиком: STARTTLS, авторизация, конверт, тело письма.

func TestSMTPMailerSendsVerification(t *testing.T) {
	srv := startFakeSMTP(t)

	m := NewSMTPMailer(config.MailConfig{
		Host:     "127.0.0.1",
		Port:     srv.port,
		Username: "robot@example.com",
		Password: "secret",
		From:     "robot@example.com",
		FromName: "На родном",
		Timeout:  10 * time.Second,
	}, "https://example.com/")
	m.tlsConfig = &tls.Config{RootCAs: srv.pool, ServerName: "127.0.0.1"}

	if err := m.SendVerification(context.Background(), "teacher@school.ru", "Алия <Гарипова>", "TOK EN/1+2"); err != nil {
		t.Fatalf("письмо не отправилось: %v", err)
	}

	got := srv.wait(t)

	if got.from != "robot@example.com" {
		t.Errorf("MAIL FROM = %q", got.from)
	}
	if got.to != "teacher@school.ru" {
		t.Errorf("RCPT TO = %q", got.to)
	}
	if !got.authenticated {
		t.Error("клиент не прошёл авторизацию на сервере")
	}
	if !got.startedTLS {
		t.Error("письмо ушло без STARTTLS")
	}

	// Ссылка обязана быть собрана из PUBLIC_URL и содержать токен в
	// экранированном виде: иначе пробел и плюс в токене ломают адрес.
	wantLink := "https://example.com/verify?token=TOK+EN%2F1%2B2"
	if !strings.Contains(got.body, wantLink) {
		t.Errorf("в письме нет правильной ссылки %q", wantLink)
	}

	// Обе части обязательны: часть почтовых клиентов режет HTML.
	if !strings.Contains(got.body, "Content-Type: text/plain; charset=utf-8") {
		t.Error("в письме нет текстовой части")
	}
	if !strings.Contains(got.body, "Content-Type: text/html; charset=utf-8") {
		t.Error("в письме нет HTML-части")
	}

	// Имя вводит пользователь. В HTML-части оно обязано быть экранировано;
	// в текстовой сырое имя — норма, там разметки нет.
	html := htmlPart(t, got.body)
	if strings.Contains(html, "Алия <Гарипова>") {
		t.Error("имя попало в HTML без экранирования — это дыра для разметки")
	}
	if !strings.Contains(html, "Алия &lt;Гарипова&gt;") {
		t.Error("не нашли экранированное имя в HTML-части")
	}

	// Заголовки с кириллицей должны быть закодированы, иначе часть серверов
	// режет письмо или показывает кракозябры.
	if !strings.Contains(got.body, "Subject: =?utf-8?q?") {
		t.Error("тема письма не закодирована")
	}
}

func TestSMTPMailerRefusesWithoutTLS(t *testing.T) {
	srv := startFakeSMTP(t)
	srv.noTLS = true

	m := NewSMTPMailer(config.MailConfig{
		Host: "127.0.0.1", Port: srv.port, From: "robot@example.com", Timeout: 5 * time.Second,
	}, "https://example.com")

	err := m.SendVerification(context.Background(), "teacher@school.ru", "Алия", "t")
	if err == nil {
		t.Fatal("письмо ушло по открытому каналу — пароль от почты так утекает")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("ожидали отказ из-за STARTTLS, получили: %v", err)
	}
}

// Адрес получателя подставляется в заголовок To. Перевод строки в нём
// позволил бы дописать свои заголовки и разослать письмо кому угодно от
// нашего имени. Защита стоит в валидации email — здесь она закрепляется.
func TestEmailWithNewlineIsRejectedBeforeSending(t *testing.T) {
	injected := "victim@school.ru\r\nBcc: everyone@school.ru"
	if err := validateRegistration(injected, "LongEnoughPass1", "Имя"); err == nil {
		t.Fatal("адрес с переводом строки прошёл валидацию — так подставляются чужие заголовки")
	}
}

// Заявку читает тот, кто решает. Если в письме не будет данных заявителя или
// рабочей ссылки, решение принимать не по чему — и регистрация встанет молча.
func TestSMTPMailerSendsApprovalRequest(t *testing.T) {
	srv := startFakeSMTP(t)

	m := NewSMTPMailer(config.MailConfig{
		Host: "127.0.0.1", Port: srv.port, From: "robot@example.com",
		FromName: "На родном", Timeout: 10 * time.Second,
	}, "https://example.com/")
	m.tlsConfig = &tls.Config{RootCAs: srv.pool, ServerName: "127.0.0.1"}

	app := Applicant{
		Name:  "Алия <Гарипова>",
		Email: "teacher@school.ru",
		Org:   "Школа № 7",
	}
	if err := m.SendApprovalRequest(context.Background(), "admin@example.com", app, "TOK EN/1+2"); err != nil {
		t.Fatalf("заявка не отправилась: %v", err)
	}

	got := srv.wait(t)
	if got.to != "admin@example.com" {
		t.Errorf("заявка ушла не рассматривающему: RCPT TO = %q", got.to)
	}

	wantLink := "https://example.com/approve?token=TOK+EN%2F1%2B2"
	if !strings.Contains(got.body, wantLink) {
		t.Errorf("в письме нет ссылки на заявку %q", wantLink)
	}
	if !strings.Contains(got.body, app.Email) {
		t.Error("в письме нет адреса заявителя — решать не по чему")
	}
	if !strings.Contains(got.body, "text/plain") || !strings.Contains(got.body, "text/html") {
		t.Error("письмо не двухчастное")
	}

	// Имя и организацию вводит человек: в HTML они обязаны быть экранированы.
	html := htmlPart(t, got.body)
	if strings.Contains(html, "Алия <Гарипова>") {
		t.Error("имя заявителя попало в HTML без экранирования")
	}
	if !strings.Contains(html, "Школа № 7") {
		t.Error("в HTML-части нет организации заявителя")
	}
}

// После одобрения человек должен понять, что делать дальше, — поэтому в
// письме ссылка на вход, а не «зайдите на сайт».
func TestApprovedMailInvitesToLogIn(t *testing.T) {
	srv := startFakeSMTP(t)

	m := NewSMTPMailer(config.MailConfig{
		Host: "127.0.0.1", Port: srv.port, From: "robot@example.com", Timeout: 10 * time.Second,
	}, "https://example.com/")
	m.tlsConfig = &tls.Config{RootCAs: srv.pool, ServerName: "127.0.0.1"}

	if err := m.SendAccountApproved(context.Background(), "teacher@school.ru", "Алия"); err != nil {
		t.Fatalf("письмо об одобрении не отправилось: %v", err)
	}

	got := srv.wait(t)
	if !strings.Contains(got.body, "https://example.com/login") {
		t.Error("в письме об одобрении нет ссылки на вход")
	}
	if !strings.Contains(got.body, "text/plain") || !strings.Contains(got.body, "text/html") {
		t.Error("письмо не двухчастное")
	}
}

// Отказ не должен содержать ссылок на вход: вход закрыт, и звать туда — это
// отправить человека на «Доступ закрыт».
func TestRejectedMailHasNoLoginLink(t *testing.T) {
	if strings.Contains(rejectedText("Алия"), "/login") ||
		strings.Contains(rejectedHTML("Алия"), "/login") {
		t.Error("в письме об отказе есть ссылка на вход")
	}
}

// Ссылка одобрения на стенде без почты нужна по той же причине, что и ссылка
// подтверждения: иначе заявку некому увидеть и сценарий не пройти.
func TestLogMailerExposesApprovalLink(t *testing.T) {
	var dev Mailer = LogMailer{PublicURL: "http://localhost:3000"}
	link, ok := dev.(DevLinkMailer)
	if !ok {
		t.Fatal("LogMailer перестал быть DevLinkMailer")
	}
	if got := link.ApprovalLink("abc"); got != "http://localhost:3000/approve?token=abc" {
		t.Errorf("ссылка одобрения = %q", got)
	}
}

func TestPasswordResetLinkPointsToResetScreen(t *testing.T) {
	m := NewSMTPMailer(config.MailConfig{}, "https://example.com/")
	if got := m.link("/password-reset", "abc"); got != "https://example.com/password-reset?token=abc" {
		t.Errorf("ссылка смены пароля = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Поддельный SMTP-сервер
// ---------------------------------------------------------------------------

type fakeSMTP struct {
	port  int
	pool  *x509.CertPool
	cert  tls.Certificate
	noTLS bool
	done  chan received
}

type received struct {
	from, to, body string
	authenticated  bool
	startedTLS     bool
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()

	cert, pool := selfSignedCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("не удалось открыть порт: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	srv := &fakeSMTP{
		port: ln.Addr().(*net.TCPAddr).Port,
		pool: pool,
		cert: cert,
		done: make(chan received, 1),
	}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		srv.handle(conn)
	}()

	return srv
}

func (s *fakeSMTP) wait(t *testing.T) received {
	t.Helper()
	select {
	case r := <-s.done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не получил письмо за 5 секунд")
		return received{}
	}
}

// handle разыгрывает минимальный SMTP-диалог: ровно столько, сколько нужно
// клиенту из stdlib.
func (s *fakeSMTP) handle(conn net.Conn) {
	var got received
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	write := func(line string) {
		w.WriteString(line + "\r\n")
		w.Flush()
	}

	write("220 fake ESMTP")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))

		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if got.startedTLS || s.noTLS {
				write("250-fake")
				write("250 AUTH PLAIN")
			} else {
				write("250-fake")
				write("250-STARTTLS")
				write("250 AUTH PLAIN")
			}

		case cmd == "STARTTLS":
			write("220 go ahead")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
			got.startedTLS = true

		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			payload := strings.TrimSpace(strings.TrimPrefix(cmd, "AUTH PLAIN"))
			if _, err := base64.StdEncoding.DecodeString(payload); err == nil {
				got.authenticated = true
			}
			write("235 ok")

		case strings.HasPrefix(cmd, "MAIL FROM:"):
			got.from = trimAddr(line[len("MAIL FROM:"):])
			write("250 ok")

		case strings.HasPrefix(cmd, "RCPT TO:"):
			got.to = trimAddr(line[len("RCPT TO:"):])
			write("250 ok")

		case cmd == "DATA":
			write("354 send it")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				body.WriteString(l)
			}
			got.body = body.String()
			write("250 queued")

		case cmd == "QUIT":
			write("221 bye")
			s.done <- got
			return

		default:
			write("250 ok")
		}
	}
}

// htmlPart вырезает HTML-часть многочастного письма.
func htmlPart(t *testing.T, body string) string {
	t.Helper()
	const marker = "Content-Type: text/html; charset=utf-8"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("в письме нет HTML-части")
	}
	return body[i+len(marker):]
}

func trimAddr(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	if i := strings.Index(s, ">"); i >= 0 {
		s = s[:i]
	}
	return s
}

func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("не удалось сгенерировать ключ: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("не удалось выпустить сертификат: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("не удалось разобрать сертификат: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// Ссылка подтверждения в ответе API — это удобство разработки и показа.
// Утечь на прод она не должна ни при каких условиях, поэтому проверяем
// главное: настоящий отправитель такой возможности не даёт.
func TestOnlyLogMailerExposesVerificationLink(t *testing.T) {
	var real Mailer = NewSMTPMailer(config.MailConfig{Host: "smtp.example.com"}, "https://example.com")
	if _, ok := real.(DevLinkMailer); ok {
		t.Fatal("SMTPMailer отдаёт ссылку в ответ API — так она утечёт на прод")
	}

	var dev Mailer = LogMailer{PublicURL: "http://localhost:3000"}
	link, ok := dev.(DevLinkMailer)
	if !ok {
		t.Fatal("LogMailer не отдаёт ссылку — сценарий регистрации не пройти без доступа к логам")
	}
	if got := link.VerificationLink("abc"); got != "http://localhost:3000/verify?token=abc" {
		t.Errorf("ссылка = %q", got)
	}
}
