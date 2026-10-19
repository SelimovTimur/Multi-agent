package config

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
	// База часовых поясов вшивается в бинарник: SCHOOL_TZ должен разбираться
	// одинаково в любом образе, в том числе без пакета tzdata.
	_ "time/tzdata"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	envsettings "github.com/NATIVE/NATIVE/internal/platform/settings"
)

// Platform — настройки инфраструктуры нового контура: база, логи, аутентификация,
// пулы ML-шлюза.
//
// Вынесено отдельно от Config намеренно: старый контур (in-memory сессии,
// demo-логины) продолжает работать на Config, пока идёт переезд. Когда
// переезд закончится, Config схлопнется в Platform.
//
// Все значения читаются из окружения: переход с пилотной ёмкости на целевую —
// это изменение переменных и числа реплик, а не правка кода (инвариант И-4).
type Platform struct {
	// Env: "dev" или "prod". Влияет на формат логов и строгость cookie.
	Env         string
	LogLevel    string
	LogFormat   string
	MetricsAddr string

	DB   pg.Config
	Auth AuthConfig
	ML   MLConfig

	// PublicURL — адрес, по которому сервис открывается снаружи.
	// Из него собираются ссылки в письмах: сервер не может узнать свой
	// внешний адрес из запроса — за прокси там что угодно, и подставлять
	// Host из заголовка значит дать любому прислать себе письмо со ссылкой
	// на чужой домен.
	PublicURL string

	// LLMServiceURL — адрес Python-сервиса с движком разговора.
	LLMServiceURL string
	// LLMTimeout ограничивает весь вызов Python-сервиса. Таймаут одного
	// запроса к провайдеру задаётся отдельно в Python через
	// LLM_REQUEST_TIMEOUT_SECONDS.
	LLMTimeout time.Duration

	Mail MailConfig

	// TrustedProxies — из каких адресов мы верим заголовкам X-Forwarded-For
	// и X-Real-IP. Всё остальное считается адресом самого клиента.
	// Без этого списка счётчик попыток подбора обходится подделкой заголовка.
	TrustedProxies []netip.Prefix

	// MigrateOnStart — применять миграции при старте API.
	// В dev удобно; на проде выключено: миграции гоняет отдельный шаг выката,
	// иначе две реплики стартуют одновременно и дерутся за схему.
	MigrateOnStart bool

	// RequireDBVersion — не стартовать, если есть неприменённые миграции.
	// Защищает от выката кода на старую схему.
	RequireDBVersion bool

	// SchoolTZ — часовой пояс школы (SCHOOL_TZ), имя из базы IANA.
	// «Выдано на уроке до конца дня» и «на дом до 03.10» — это сутки школы,
	// а не сервера: контейнер живёт в UTC.
	SchoolTZ string
}

// DefaultSchoolTZ — часовой пояс школы, если SCHOOL_TZ не задан.
const DefaultSchoolTZ = "Europe/Moscow"

// SchoolLocation разбирает SCHOOL_TZ. Ошибка — опечатка в окружении: со
// сдвинутыми на три часа сроками лучше не стартовать вовсе.
func (p Platform) SchoolLocation() (*time.Location, error) {
	name := strings.TrimSpace(p.SchoolTZ)
	if name == "" {
		name = DefaultSchoolTZ
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("config: неизвестный часовой пояс SCHOOL_TZ=%q: %w", name, err)
	}
	return loc, nil
}

// AuthConfig — параметры аутентификации.
type AuthConfig struct {
	// Сессия учителя живёт 30 дней и продлевается при активности (NR-8).
	SessionTTL     time.Duration
	SessionCookie  string
	CookieSecure   bool
	CookieDomain   string
	StudentCookie  string
	StudentTTL     time.Duration
	VerifyTokenTTL time.Duration
	ResetTokenTTL  time.Duration

	// ApproveTokenTTL — сколько живёт ссылка одобрения заявки. Дни, а не часы:
	// заявку рассматривает человек, и письмо вполне может подождать выходных.
	ApproveTokenTTL time.Duration

	// AutoApproveEmails — адреса, которым одобрение не нужно: они становятся
	// активными сразу после подтверждения почты. Так команда заходит на стенд,
	// не дожидаясь письма самой себе. Хранятся приведёнными к нижнему регистру.
	AutoApproveEmails []string

	// ApproverEmails — кому уходит заявка на рассмотрение. Пустой список
	// означает, что письмо слать некому: сервис об этом предупреждает в логе,
	// а заявка остаётся ждать в базе.
	ApproverEmails []string

	// Защита от подбора пароля: N неудачных попыток по паре (email, IP)
	// закрывают вход на Window. Это НЕ продуктовая квота — продуктовые
	// лимиты считаются по org_id и user_id (инвариант И-9).
	LoginMaxAttempts int
	LoginWindow      time.Duration

	// Параметры argon2id. Память в килобайтах.
	ArgonTime    uint32
	ArgonMemory  uint32
	ArgonThreads uint8
	ArgonKeyLen  uint32
}

// MailConfig — доступ к почтовому серверу.
//
// Обычный SMTP, а не API конкретного провайдера: так подходит и Яндекс 360,
// и Mail.ru, и корпоративный сервер школы — меняются только переменные.
type MailConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// From — адрес отправителя. Обычно совпадает с Username, но у части
	// провайдеров это разные вещи (логин — учётка, From — алиас).
	From     string
	FromName string
	// Timeout на всю отправку: залипшее соединение с почтовым сервером не
	// должно держать горутину бесконечно.
	Timeout time.Duration
}

// Configured сообщает, есть ли куда отправлять почту.
func (m MailConfig) Configured() bool {
	return strings.TrimSpace(m.Host) != "" && strings.TrimSpace(m.From) != ""
}

// MLConfig — ёмкость пулов ML-шлюза и приоритеты.
//
// Пулы независимы: чат ученика не должен ждать, пока учитель догенерирует урок.
// Раньше это был один семафор на 20 для всего сразу.
type MLConfig struct {
	LLMChatConcurrency  int
	LLMBuildConcurrency int
	ASRConcurrency      int
	TTSConcurrency      int
	MediaConcurrency    int

	// Максимальное ожидание слота в пуле. По истечении задача отклоняется с
	// честным сообщением, а не висит молча.
	AcquireTimeout time.Duration

	// Стоимость модели в миллионных долях рубля за токен. Нужна, чтобы
	// ops.pipeline_calls отвечал на вопрос «сколько стоил вот этот урок».
	CostInputMicroPerToken  int64
	CostOutputMicroPerToken int64
}

// LoadPlatform читает настройки инфраструктуры из окружения.
func LoadPlatform() Platform {
	env := strings.ToLower(envsettings.String("APP_ENV"))
	isProd := env == "prod" || env == "production"

	logFormat := envsettings.String("LOG_FORMAT")
	if logFormat == "" {
		if isProd {
			logFormat = "json"
		} else {
			logFormat = "text"
		}
	}

	mail := MailConfig{
		Host:     envsettings.String("SMTP_HOST"),
		Port:     envsettings.Int("SMTP_PORT"),
		Username: envsettings.String("SMTP_USERNAME"),
		Password: envsettings.String("SMTP_PASSWORD"),
		From:     envOrDefault("SMTP_FROM", envsettings.String("SMTP_USERNAME")),
		FromName: envsettings.String("SMTP_FROM_NAME"),
		Timeout:  time.Duration(envsettings.Int("SMTP_TIMEOUT_SEC")) * time.Second,
	}

	return Platform{
		Env:         env,
		LogLevel:    envsettings.String("LOG_LEVEL"),
		LogFormat:   logFormat,
		MetricsAddr: envsettings.String("METRICS_ADDR"),

		DB: pg.Config{
			DSN:              envsettings.String("DATABASE_URL"),
			MaxConns:         int32(envsettings.Int("DB_MAX_CONNS")),
			MinConns:         int32(envsettings.Int("DB_MIN_CONNS")),
			MaxConnLifetime:  time.Duration(envsettings.Int("DB_CONN_LIFETIME_MIN")) * time.Minute,
			MaxConnIdleTime:  time.Duration(envsettings.Int("DB_CONN_IDLE_MIN")) * time.Minute,
			ConnectTimeout:   time.Duration(envsettings.Int("DB_CONNECT_TIMEOUT_SEC")) * time.Second,
			StatementTimeout: time.Duration(envsettings.Int("DB_STATEMENT_TIMEOUT_SEC")) * time.Second,
		},

		Auth: AuthConfig{
			SessionTTL:     time.Duration(envsettings.Int("AUTH_SESSION_TTL_DAYS")) * 24 * time.Hour,
			SessionCookie:  envsettings.String("AUTH_SESSION_COOKIE"),
			CookieSecure:   envsettings.BoolOr("AUTH_COOKIE_SECURE", isProd),
			CookieDomain:   envsettings.String("AUTH_COOKIE_DOMAIN"),
			StudentCookie:  envsettings.String("AUTH_STUDENT_COOKIE"),
			StudentTTL:     time.Duration(envsettings.Int("AUTH_STUDENT_TTL_DAYS")) * 24 * time.Hour,
			VerifyTokenTTL: time.Duration(envsettings.Int("AUTH_VERIFY_TTL_HOURS")) * time.Hour,
			ResetTokenTTL:  time.Duration(envsettings.Int("AUTH_RESET_TTL_MIN")) * time.Minute,

			ApproveTokenTTL:   time.Duration(envsettings.Int("AUTH_APPROVE_TTL_DAYS")) * 24 * time.Hour,
			AutoApproveEmails: emailList(envsettings.String("AUTH_AUTO_APPROVE_EMAILS")),
			// Некому рассматривать заявки — пишем на адрес отправителя: ящик
			// платформы читает тот же человек, который её и запускает. Это
			// лучше, чем потерять заявку вовсе из-за незаполненной переменной.
			ApproverEmails: fallbackList(emailList(envsettings.String("AUTH_APPROVER_EMAILS")), mail.From),

			LoginMaxAttempts: envsettings.Int("AUTH_LOGIN_MAX_ATTEMPTS"),
			LoginWindow:      time.Duration(envsettings.Int("AUTH_LOGIN_WINDOW_MIN")) * time.Minute,

			// Дефолты argon2id: ~64 МБ и 3 прохода. Держит подбор дорогим,
			// но укладывается в бюджет запроса на входе.
			ArgonTime:    uint32(envsettings.Int("AUTH_ARGON_TIME")),
			ArgonMemory:  uint32(envsettings.Int("AUTH_ARGON_MEMORY_KB")),
			ArgonThreads: uint8(envsettings.Int("AUTH_ARGON_THREADS")),
			ArgonKeyLen:  uint32(envsettings.Int("AUTH_ARGON_KEYLEN")),
		},

		ML: MLConfig{
			LLMChatConcurrency:  envsettings.Int("ML_LLM_CHAT_CONCURRENCY"),
			LLMBuildConcurrency: envsettings.Int("ML_LLM_BUILD_CONCURRENCY"),
			ASRConcurrency:      envsettings.Int("ML_ASR_CONCURRENCY"),
			TTSConcurrency:      envsettings.Int("ML_TTS_CONCURRENCY"),
			MediaConcurrency:    envsettings.Int("ML_MEDIA_CONCURRENCY"),
			AcquireTimeout:      time.Duration(envsettings.Int("ML_ACQUIRE_TIMEOUT_SEC")) * time.Second,

			CostInputMicroPerToken:  int64(envsettings.Int("ML_COST_IN_MICRO_PER_TOKEN")),
			CostOutputMicroPerToken: int64(envsettings.Int("ML_COST_OUT_MICRO_PER_TOKEN")),
		},

		PublicURL: strings.TrimRight(envsettings.String("PUBLIC_URL"), "/"),

		LLMServiceURL: envsettings.String("LLM_SERVICE_URL"),
		LLMTimeout:    time.Duration(envsettings.Int("LLM_SERVICE_TIMEOUT_SECONDS")) * time.Second,

		Mail: mail,

		TrustedProxies: trustedProxies(),

		MigrateOnStart:   envsettings.BoolOr("DB_MIGRATE_ON_START", !isProd),
		RequireDBVersion: envsettings.BoolOr("DB_REQUIRE_VERSION", true),

		SchoolTZ: envsettings.String("SCHOOL_TZ"),
	}
}

// Enabled сообщает, подключён ли новый контур. Пока DATABASE_URL не задан,
// приложение работает по-старому — это даёт переезжать по частям, а не
// одним выкатом (двойной контур из NR-2).
func (p Platform) Enabled() bool { return strings.TrimSpace(p.DB.DSN) != "" }

// emailList разбирает список адресов через запятую.
//
// Регистр приводится сразу: дальше адреса сравниваются с email_normalized, и
// «Ivan@School.ru» в переменной окружения не должен означать другой аккаунт,
// чем «ivan@school.ru» в базе.
func emailList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if e := strings.ToLower(strings.TrimSpace(part)); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// fallbackList подставляет запасной адрес, когда список пуст.
func fallbackList(list []string, fallback string) []string {
	if len(list) > 0 {
		return list
	}
	return emailList(fallback)
}

// trustedProxies читает TRUSTED_PROXIES или отдаёт приватные диапазоны.
//
// Дефолт рассчитан на нашу схему: один nginx в docker-сети. Если приложение
// когда-нибудь встанет за внешним балансировщиком, его адрес нужно добавить
// в TRUSTED_PROXIES явно — иначе все попытки входа будут считаться от одного
// адреса балансировщика и один подборщик заблокирует вход всем.
func trustedProxies() []netip.Prefix {
	if raw := strings.TrimSpace(envsettings.String("TRUSTED_PROXIES")); raw != "" {
		if list := httpx.ParseTrustedProxies(raw); len(list) > 0 {
			return list
		}
	}
	return httpx.DefaultTrustedProxies
}
