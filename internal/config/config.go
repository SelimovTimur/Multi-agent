package config

import (
	"time"

	envsettings "github.com/NATIVE/NATIVE/internal/platform/settings"
)

// Config — настройки HTTP-сервера и клиентов речевых сервисов.
// Значения берутся из переменных окружения, умолчания — в settings.py.
type Config struct {
	// Ключ OpenAI для голосовой сессии GPT Live. Только на сервере, в JSON
	// не уходит.
	OpenAIAPIKey string `json:"-"`

	Port            string        // порт, на котором слушает API
	ReadTimeout     time.Duration // таймаут на чтение запроса
	WriteTimeout    time.Duration // таймаут на запись ответа
	ShutdownTimeout time.Duration // время на корректное завершение при остановке

	// Адрес Python-сервиса с движком разговора.
	LLMServiceURL string
	// Адрес сервиса распознавания речи.
	ASRServiceURL string
	// Адрес сервиса синтеза речи.
	TTSServiceURL string

	// Сколько одновременных запросов допускается к каждому сервису: если
	// пришло 100 диктовок, MaxConcurrentASR обрабатываются, остальные ждут.
	MaxConcurrentASR int
	MaxConcurrentTTS int

	MaxAudioSec int // максимальная длительность записи ученика (секунды)
}

// Load читает переменные окружения и возвращает готовую конфигурацию.
func Load() *Config {
	return &Config{
		OpenAIAPIKey:    envsettings.String("OPENAI_API_KEY"),
		Port:            envsettings.String("PORT"),
		ReadTimeout:     time.Duration(envsettings.Int("API_READ_TIMEOUT_SECONDS")) * time.Second,
		WriteTimeout:    time.Duration(envsettings.Int("API_WRITE_TIMEOUT_SECONDS")) * time.Second,
		ShutdownTimeout: time.Duration(envsettings.Int("API_SHUTDOWN_TIMEOUT_SECONDS")) * time.Second,

		LLMServiceURL: envsettings.String("LLM_SERVICE_URL"),
		ASRServiceURL: envsettings.String("ASR_SERVICE_URL"),
		TTSServiceURL: envsettings.String("TTS_SERVICE_URL"),

		MaxConcurrentASR: envsettings.Int("MAX_CONCURRENT_ASR"),
		MaxConcurrentTTS: envsettings.Int("MAX_CONCURRENT_TTS"),

		MaxAudioSec: envsettings.Int("MAX_AUDIO_SECONDS"),
	}
}

func envOrDefault(key, def string) string {
	if v := envsettings.String(key); v != "" {
		return v
	}
	return def
}
