package config

import (
	"testing"
	"time"
)

func TestLoadUsesLLMMicroservice(t *testing.T) {
	t.Setenv("LLM_SERVICE_URL", "")
	cfg := Load()
	if cfg.LLMServiceURL != "http://llm:8000" {
		t.Fatalf("LLMServiceURL = %q", cfg.LLMServiceURL)
	}
	if cfg.TTSServiceURL != "http://tts:8000" {
		t.Fatalf("TTSServiceURL = %q", cfg.TTSServiceURL)
	}
}

func TestLessonServiceTimeoutIsSeparateFromProviderTimeout(t *testing.T) {
	t.Setenv("LLM_REQUEST_TIMEOUT_SECONDS", "30")
	t.Setenv("LLM_SERVICE_TIMEOUT_SECONDS", "")
	if got := LoadPlatform().LLMTimeout; got != 15*time.Minute {
		t.Fatalf("таймаут одного запроса обрезал сборку: %v", got)
	}
	t.Setenv("LLM_SERVICE_TIMEOUT_SECONDS", "600")
	if got := LoadPlatform().LLMTimeout; got != 10*time.Minute {
		t.Fatalf("переопределение таймаута этапа: %v", got)
	}
}

// Сроки выдач считаются в сутках школы. Опечатка в SCHOOL_TZ — отказ
// старта, а не молча сдвинутые на три часа сроки.
func TestSchoolTimezone(t *testing.T) {
	t.Setenv("SCHOOL_TZ", "")
	loc, err := LoadPlatform().SchoolLocation()
	if err != nil || loc.String() != "Europe/Moscow" {
		t.Fatalf("по умолчанию — Москва: %v %v", loc, err)
	}

	t.Setenv("SCHOOL_TZ", "Asia/Yekaterinburg")
	loc, err = LoadPlatform().SchoolLocation()
	if err != nil || loc.String() != "Asia/Yekaterinburg" {
		t.Fatalf("SCHOOL_TZ из окружения: %v %v", loc, err)
	}

	t.Setenv("SCHOOL_TZ", "Europe/Kazn")
	if _, err := LoadPlatform().SchoolLocation(); err == nil {
		t.Fatal("неизвестный часовой пояс должен останавливать старт")
	}
}
