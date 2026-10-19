package settings

import "testing"

func TestOverridesAndEnvironmentDependentFlags(t *testing.T) {
	t.Setenv("LIVE_MODEL", "custom-live")
	if String("LIVE_MODEL") != "custom-live" {
		t.Fatal("модель из окружения должна перекрывать общее значение")
	}
	t.Setenv("ML_ASR_CONCURRENCY", "7")
	if Int("ML_ASR_CONCURRENCY") != 7 {
		t.Fatal("лимит из окружения не применён")
	}
	t.Setenv("AUTH_COOKIE_SECURE", "")
	if !BoolOr("AUTH_COOKIE_SECURE", true) || BoolOr("AUTH_COOKIE_SECURE", false) {
		t.Fatal("пустая настройка должна сохранить выбор по окружению приложения")
	}
	t.Setenv("AUTH_COOKIE_SECURE", "false")
	if BoolOr("AUTH_COOKIE_SECURE", true) {
		t.Fatal("явное false должно перекрывать значение по окружению")
	}
}
