package learning

import "testing"

// Голосовая сессия платная, а звук идёт мимо сервера: сколько ученик
// наговорил, сервер знает только со слов браузера. Поэтому пределы здесь —
// не про удобство, а про счёт от провайдера.

func TestLiveSessionsAreBoundedButLeaveRoomForReconnects(t *testing.T) {
	cases := []struct{ turns, want int }{
		{8, 12},  // обычный урок: обрывов связи хватит с запасом
		{3, 7},   // самый короткий разговор
		{20, 24}, // самый длинный
		{0, 12},  // длина не задана — считаем от умолчания
	}
	for _, c := range cases {
		if got := maxLiveSessions(c.turns); got != c.want {
			t.Errorf("%d реплик → %d сессий, ожидали %d", c.turns, got, c.want)
		}
	}
}

// Потолок реплик — про журнал и деньги, а не про урок: дойдя до длины
// разговора, аватар прощается, но браузер может слать расшифровку и дальше.
func TestLiveTurnsCapIsAboveConversationLength(t *testing.T) {
	for _, turns := range []int{3, 8, 20} {
		cap := liveTurnsCap(turns)
		if cap <= turns {
			t.Errorf("потолок %d не больше длины разговора %d", cap, turns)
		}
	}
	if liveTurnsCap(0) != defaultAvatarTurns*3 {
		t.Errorf("без длины разговора потолок считается от умолчания: %d", liveTurnsCap(0))
	}
}
