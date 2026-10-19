package live

import (
	"encoding/json"
	"testing"
)

func TestLiveModelOverridesApplyToConversation(t *testing.T) {
	t.Setenv("LIVE_MODEL", "test-live")
	t.Setenv("LIVE_BACKEND_MODEL", "test-backend")
	t.Setenv("LIVE_REASONING_EFFORT", "low")
	raw, err := sessionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	var session map[string]any
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	backend := session["delegation"].(map[string]any)["responses"].(map[string]any)
	if session["model"] != "test-live" || backend["model"] != "test-backend" {
		t.Fatal("переопределение моделей не применено")
	}
	if backend["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatal("переопределение глубины рассуждения не применено")
	}
	if session["instructions"] == "" || backend["instructions"] == "" {
		t.Fatal("утеряны инструкции")
	}
	if backend["tool_choice"] != "none" {
		t.Fatal("добавлены инструменты в обычный разговор")
	}
}
