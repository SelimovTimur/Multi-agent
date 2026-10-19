package content

import "testing"

// Переходы статусов версии — обещание, данное классам, которым урок выдан.
// Опубликованную версию нельзя вернуть в черновик: назначения ссылаются
// именно на неё, и подмена содержимого задним числом недопустима.

func TestVersionTransitions(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{VersionDraft, VersionReady, true},
		{VersionDraft, VersionArchived, true},
		{VersionDraft, VersionPublished, false}, // только через ready
		{VersionReady, VersionPublished, true},
		{VersionReady, VersionDraft, true}, // вернуть в работу можно
		{VersionPublished, VersionArchived, true},
		{VersionPublished, VersionDraft, false},
		{VersionPublished, VersionReady, false},
		{VersionArchived, VersionDraft, false},
		{VersionArchived, VersionPublished, false},
	}

	for _, c := range cases {
		if got := canTransition(c.from, c.to); got != c.want {
			t.Errorf("%s → %s = %v, ожидали %v", c.from, c.to, got, c.want)
		}
	}
}

// Опубликованная версия — тупик, кроме архивации. Если этот тест упал,
// значит кто-то открыл дорогу к правке выданного урока.
func TestPublishedVersionIsDeadEnd(t *testing.T) {
	allowed := AllowedNext(VersionPublished)
	if len(allowed) != 1 || allowed[0] != VersionArchived {
		t.Errorf("из published можно только в archived, получили %v", allowed)
	}
}

func TestArchivedHasNoWayBack(t *testing.T) {
	if len(AllowedNext(VersionArchived)) != 0 {
		t.Errorf("из archived переходов быть не должно, получили %v", AllowedNext(VersionArchived))
	}
}

// Название и уровень обязательны: урок без них не найти в библиотеке.
func TestDraftValidation(t *testing.T) {
	if _, err := (DraftContent{LangCode: "tatar", Level: "A2"}).validate(); err == nil {
		t.Error("урок без названия должен отклоняться")
	}
	if _, err := (DraftContent{Title: "Урок", Level: "A2"}).validate(); err == nil {
		t.Error("урок без языка должен отклоняться")
	}
	if _, err := (DraftContent{Title: "Урок", LangCode: "tatar", Level: "C2"}).validate(); err == nil {
		t.Error("несуществующий уровень должен отклоняться")
	}
	d, err := (DraftContent{Title: "  Урок  ", LangCode: " TATAR ", Level: " a2 "}).validate()
	if err != nil {
		t.Fatalf("корректный урок отклонён: %v", err)
	}
	if d.Title != "Урок" || d.LangCode != "tatar" || d.Level != "A2" {
		t.Errorf("значения не нормализованы: %+v", d)
	}
}
