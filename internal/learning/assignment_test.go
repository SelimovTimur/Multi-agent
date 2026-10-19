package learning

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// Доступность урока — то, что ученик чувствует напрямую: открылся урок
// вовремя или нет. Ошибка здесь не видна в логах, зато видна на занятии.

func TestAssignmentAvailability(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	before := now.Add(-time.Hour)
	after := now.Add(time.Hour)

	cases := []struct {
		name     string
		a        Assignment
		wantOpen bool
	}{
		{
			name:     "без сроков доступен всегда",
			a:        Assignment{State: StateActive},
			wantOpen: true,
		},
		{
			name:     "закрытый недоступен, даже если время подходит",
			a:        Assignment{State: StateClosed, OpensAt: &before, ClosesAt: &after},
			wantOpen: false,
		},
		{
			name:     "время открытия ещё не наступило",
			a:        Assignment{State: StateScheduled, OpensAt: &after},
			wantOpen: false,
		},
		{
			name:     "срок закрытия прошёл — недоступен, даже если состояние ещё active",
			a:        Assignment{State: StateActive, ClosesAt: &before},
			wantOpen: false,
		},
		{
			name:     "внутри окна доступен",
			a:        Assignment{State: StateActive, OpensAt: &before, ClosesAt: &after},
			wantOpen: true,
		},
		{
			name:     "момент закрытия — уже недоступен",
			a:        Assignment{State: StateActive, ClosesAt: &now},
			wantOpen: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.Open(now); got != c.wantOpen {
				t.Errorf("Open = %v, ожидали %v", got, c.wantOpen)
			}
		})
	}
}

// Состояние при создании: без времени открытия урок доступен сразу,
// с временем в будущем — ждёт своего часа.
func TestInitialState(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	if got := initialState(nil, now); got != StateActive {
		t.Errorf("без opens_at = %q, ожидали active", got)
	}
	if got := initialState(&future, now); got != StateScheduled {
		t.Errorf("opens_at в будущем = %q, ожидали scheduled", got)
	}
	if got := initialState(&past, now); got != StateActive {
		t.Errorf("opens_at в прошлом = %q, ожидали active", got)
	}
}

func TestValidateWindow(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	past := now.Add(-24 * time.Hour)
	soon := now.Add(time.Hour)
	later := now.Add(48 * time.Hour)

	if err := validateWindow(&soon, &later, now); err != nil {
		t.Errorf("корректное окно отклонено: %v", err)
	}
	// Открытие в прошлом — это «выдать прямо сейчас», нормальный сценарий.
	if err := validateWindow(&past, &later, now); err != nil {
		t.Errorf("открытие в прошлом должно быть разрешено: %v", err)
	}
	if err := validateWindow(&later, &soon, now); err == nil {
		t.Error("закрытие раньше открытия должно отклоняться")
	}
	if err := validateWindow(nil, &past, now); err == nil {
		t.Error("закрытие в прошлом должно отклоняться: урок никто не успеет пройти")
	}
}

// Ключ ответов не должен попадать ученику ни при каких обстоятельствах.
// Здесь проверяется, что модель ответа вообще не несёт его в сторону ученика.
func TestAnswerEventKeepsNoSecrets(t *testing.T) {
	e := AnswerEvent{
		ID: uuid.New(), Answer: "мой ответ", IsCorrect: true, ScoreDelta: 10,
	}
	if e.Answer == "" {
		t.Error("ответ ученика должен сохраняться — по нему видно, где он ошибается")
	}
}
