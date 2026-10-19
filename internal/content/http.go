package content

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// API — HTTP-поверхность библиотеки уроков.
type API struct {
	svc   *Service
	guard *iam.Guard
}

func NewAPI(svc *Service, guard *iam.Guard) *API {
	return &API{svc: svc, guard: guard}
}

func (a *API) Mount(r chi.Router) {
	g := a.guard
	g.Handle(r, http.MethodGet, "/lessons", iam.PermLessonsOwn, a.list)
	g.Handle(r, http.MethodGet, "/lessons/{lessonID}", iam.PermLessonsOwn, a.get)
	g.Handle(r, http.MethodDelete, "/lessons/{lessonID}", iam.PermLessonsOwn, a.remove)
	g.Handle(r, http.MethodPost, "/lessons/{lessonID}/archive", iam.PermLessonsOwn, a.archive)

	g.Handle(r, http.MethodGet, "/lessons/{lessonID}/versions", iam.PermLessonsOwn, a.versions)
	g.Handle(r, http.MethodGet, "/lesson-versions/{versionID}", iam.PermLessonsOwn, a.version)
	g.Handle(r, http.MethodPost, "/lesson-versions/{versionID}/status", iam.PermLessonsOwn, a.setStatus)
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

type lessonDTO struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Lang       string `json:"lang"`
	Level      string `json:"level"`
	Status     string `json:"status"`
	SourceKind string `json:"source_kind"`

	// Version — номер последней неархивной версии, 0 если таких версий нет.
	// Строка библиотеки показывает его рядом с названием: «версия v3».
	Version int `json:"version"`
	// VersionStatus — её статус: по нему видно, можно ли урок назначать.
	VersionStatus string `json:"version_status"`
	// PublishedVersion — номер версии, которую сейчас видят классы, 0 если
	// урок ещё не публиковали. По ней строка библиотеки говорит «выдан
	// v1 · есть черновик v2» вместо «черновик» об идущем уроке.
	PublishedVersion int `json:"published_version"`
	// Classes — в какие классы урок выдан. Всегда массив, пусть и пустой:
	// null заставил бы каждую точку интерфейса проверять его отдельно.
	Classes []classRefDTO `json:"classes"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type classRefDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toLessonDTO(l Lesson) lessonDTO {
	return lessonDTO{
		ID: l.ID.String(), Title: l.Title, Lang: l.LangCode, Level: l.Level,
		Status: l.Status, SourceKind: l.SourceKind,
		Classes:   []classRefDTO{},
		CreatedAt: l.CreatedAt.Format(time.RFC3339),
		UpdatedAt: l.UpdatedAt.Format(time.RFC3339),
	}
}

// toLibraryDTO — строка библиотеки: урок вместе с версией и классами.
//
// Остальные ручки отдают урок без них: там учитель уже внутри карточки, где
// версии показаны списком, а классы — своим разделом.
func toLibraryDTO(row LibraryRow) lessonDTO {
	d := toLessonDTO(row.Lesson)
	d.Version, d.VersionStatus = row.Latest.Version, row.Latest.Status
	d.PublishedVersion = row.Published.Version
	for _, c := range row.Classes {
		d.Classes = append(d.Classes, classRefDTO{ID: c.ID.String(), Name: c.Name})
	}
	return d
}

type versionDTO struct {
	ID          string  `json:"id"`
	LessonID    string  `json:"lesson_id"`
	Version     int     `json:"version"`
	Status      string  `json:"status"`
	Title       string  `json:"title"`
	Level       string  `json:"level"`
	Note        string  `json:"note"`
	PublishedAt *string `json:"published_at"`
	CreatedAt   string  `json:"created_at"`
	// AllowedNext подсказывает интерфейсу, какие кнопки показывать активными.
	AllowedNext     []string        `json:"allowed_next"`
	QualityWarnings json.RawMessage `json:"quality_warnings,omitempty"`
}

func toVersionDTO(v LessonVersion) versionDTO {
	d := versionDTO{
		ID: v.ID.String(), LessonID: v.LessonID.String(), Version: v.Version,
		Status: v.Status, Title: v.Title, Level: v.Level, Note: v.Note,
		CreatedAt:       v.CreatedAt.Format(time.RFC3339),
		AllowedNext:     AllowedNext(v.Status),
		QualityWarnings: v.QualityWarnings,
	}
	if v.PublishedAt != nil {
		s := v.PublishedAt.Format(time.RFC3339)
		d.PublishedAt = &s
	}
	if d.AllowedNext == nil {
		d.AllowedNext = []string{}
	}
	return d
}

// contentDTO — версия со всем содержимым.
type contentDTO struct {
	Version     versionDTO     `json:"version"`
	RefinedText string         `json:"refined_text"`
	FrankText   string         `json:"frank_text"`
	Lexicon     []lexicalDTO   `json:"lexicon"`
	Grammar     []grammarDTO   `json:"grammar"`
	Exercises   []exerciseDTO  `json:"exercises"`
	Avatar      avatarDTO      `json:"avatar"`
	Objectives  []objectiveDTO `json:"objectives"`
}

// objectiveDTO — цель урока: чему ученик научится.
type objectiveDTO struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Text     string `json:"text"`
}

// avatarDTO — настройка разговора с аватаром.
//
// RolePrompt с omitempty и явным обнулением в ToStudentDTO: роль пишет
// учитель для модели, ученику она не нужна и показываться не должна.
// Пояснения к целям — так же: указатель с omitempty, учителю всегда массив
// (пусть и пустой), ученику — поля нет вовсе.
type avatarDTO struct {
	Enabled              bool      `json:"enabled"`
	RolePrompt           string    `json:"role_prompt,omitempty"`
	MaxTurns             int       `json:"max_turns"`
	PracticePhrases      []string  `json:"practice_phrases"`
	PracticeGrammar      []string  `json:"practice_grammar"`
	PracticePhraseNotes  *[]string `json:"practice_phrase_notes,omitempty"`
	PracticeGrammarNotes *[]string `json:"practice_grammar_notes,omitempty"`
	HintEnabled          bool      `json:"hint_enabled"`
	DictionaryEnabled    bool      `json:"dictionary_enabled"`
	Saved                bool      `json:"saved"`
}

// avatarInput — настройка в теле правки. Отсутствие поля «avatar» целиком
// значит «не трогать», а не «сбросить».
type avatarInput struct {
	Enabled *bool `json:"enabled"`
	// Saved — экран настройки, а не карта урока. Карта присылает false и
	// только переключает блок; списки, собранные из урока автоматически,
	// после этого не становятся выбором учителя.
	Saved           bool     `json:"saved"`
	RolePrompt      string   `json:"role_prompt"`
	MaxTurns        int      `json:"max_turns"`
	PracticePhrases []string `json:"practice_phrases"`
	PracticeGrammar []string `json:"practice_grammar"`
	// Пояснения к целям, той же длины, что списки. Указатель отличает «не
	// прислали» (старый клиент, карта урока — пояснения остаются прежними)
	// от «прислали пустой список».
	PracticePhraseNotes  *[]string `json:"practice_phrase_notes"`
	PracticeGrammarNotes *[]string `json:"practice_grammar_notes"`
	HintEnabled          bool      `json:"hint_enabled"`
	DictionaryEnabled    bool      `json:"dictionary_enabled"`
}

type lexicalDTO struct {
	ID          string `json:"id"`
	Position    int    `json:"position"`
	Phrase      string `json:"phrase"`
	Translation string `json:"translation"`
	Origin      string `json:"origin"`
}

type grammarDTO struct {
	ID          string           `json:"id"`
	Position    int              `json:"position"`
	Title       string           `json:"title"`
	Example     string           `json:"example"`
	Origin      string           `json:"origin"`
	Explanation string           `json:"explanation"`
	Examples    []GrammarExample `json:"examples"`
	Practice    []GrammarTask    `json:"practice"`
}

// exerciseDTO — упражнение.
//
// AnswerKey с omitempty и отдельным решением на каждой ручке: для ученика
// он не выбирается из базы вовсе, поэтому здесь остаётся пустым.
type exerciseDTO struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Position    int             `json:"position"`
	Title       string          `json:"title"`
	Instruction string          `json:"instruction"`
	Payload     json.RawMessage `json:"payload"`
	AnswerKey   json.RawMessage `json:"answer_key,omitempty"`
}

func toContentDTO(c VersionContent) contentDTO {
	out := contentDTO{
		Version:     toVersionDTO(c.Version),
		RefinedText: c.Version.RefinedText,
		FrankText:   c.Version.FrankText,
		Lexicon:     make([]lexicalDTO, 0, len(c.Lexicon)),
		Grammar:     make([]grammarDTO, 0, len(c.Grammar)),
		Exercises:   make([]exerciseDTO, 0, len(c.Exercises)),
		Objectives:  make([]objectiveDTO, 0, len(c.Objectives)),
		Avatar: avatarDTO{
			Enabled: c.Avatar.Enabled, RolePrompt: c.Avatar.RolePrompt, MaxTurns: c.Avatar.MaxTurns,
			PracticePhrases: nonNil(c.Avatar.PracticePhrases), PracticeGrammar: nonNil(c.Avatar.PracticeGrammar),
			PracticePhraseNotes:  notesPtr(c.Avatar.PracticePhrases, c.Avatar.PracticePhraseNotes),
			PracticeGrammarNotes: notesPtr(c.Avatar.PracticeGrammar, c.Avatar.PracticeGrammarNotes),
			HintEnabled:          c.Avatar.HintEnabled, DictionaryEnabled: c.Avatar.DictionaryEnabled,
			Saved: c.Avatar.Saved,
		},
	}
	for _, o := range c.Objectives {
		out.Objectives = append(out.Objectives, objectiveDTO{
			ID: o.ID.String(), Position: o.Position, Text: o.Text,
		})
	}
	for _, u := range c.Lexicon {
		out.Lexicon = append(out.Lexicon, lexicalDTO{
			ID: u.ID.String(), Position: u.Position, Phrase: u.Phrase,
			Translation: u.Translation, Origin: u.Origin,
		})
	}
	for _, g := range c.Grammar {
		out.Grammar = append(out.Grammar, grammarDTO{
			ID: g.ID.String(), Position: g.Position, Title: g.Title,
			Example: g.Example, Origin: g.Origin, Explanation: g.Explanation,
			Examples: nonNilExamples(g.Examples), Practice: nonNilTasks(g.Practice),
		})
	}
	for _, e := range c.Exercises {
		dto := exerciseDTO{
			ID: e.ID.String(), Type: e.Type, Position: e.Position,
			Title: e.Title, Instruction: e.Instruction,
			Payload: json.RawMessage(e.Payload),
		}
		// Пустой объект означает, что ключ не выбирался — не отдаём поле вовсе.
		if len(e.AnswerKey) > 0 && string(e.AnswerKey) != "{}" {
			dto.AnswerKey = json.RawMessage(e.AnswerKey)
		}
		out.Exercises = append(out.Exercises, dto)
	}
	return out
}

// ToStudentDTO собирает версию для ученика.
//
// Экспортирована, чтобы её мог позвать адаптер в internal/app: содержимое
// урока ученику отдаёт модуль learning (у него право доступа через
// попытку), а знание о форме урока остаётся здесь.
//
// Ключ ответов сюда не попадает: он не выбран из базы вызывающим кодом.
func ToStudentDTO(c VersionContent) any {
	d := toContentDTO(c)
	// Замечания могут содержать правильный ответ и служебные пояснения
	// проверки: они предназначены только для учителя.
	d.Version.QualityWarnings = nil
	// Подстраховка на случай, если вызывающий всё же прочитал версию с
	// ключами: ученику они не уходят ни при каких обстоятельствах.
	for i := range d.Exercises {
		d.Exercises[i].AnswerKey = nil
	}
	d.Avatar.RolePrompt = ""
	// Пояснения к целям учитель пишет для модели и для себя: ученику они
	// подсказали бы, к чему ведёт разговор и что в нём засчитают.
	d.Avatar.PracticePhraseNotes, d.Avatar.PracticeGrammarNotes = nil, nil
	// Ответы к заданиям разбора — для учителя. Ученику остаётся сам разбор:
	// объяснение и примеры, а проверяемые задания живут в упражнениях.
	for i := range d.Grammar {
		for j := range d.Grammar[i].Practice {
			d.Grammar[i].Practice[j].Answer = ""
		}
	}
	return d
}

// draftInput — тело для создания урока и версии.
type draftInput struct {
	PreserveContentIDs bool            `json:"preserve_content_ids"`
	QualityWarnings    json.RawMessage `json:"quality_warnings"`
	Title              string          `json:"title"`
	Lang               string          `json:"lang"`
	Level              string          `json:"level"`
	SourceKind         string          `json:"source_kind"`
	RefinedText        string          `json:"refined_text"`
	FrankText          string          `json:"frank_text"`
	GrammarRaw         string          `json:"grammar_raw"`
	Note               string          `json:"note"`
	Lexicon            []struct {
		Phrase      string `json:"phrase"`
		Translation string `json:"translation"`
		Origin      string `json:"origin"`
	} `json:"lexicon"`
	Grammar []struct {
		Title       string           `json:"title"`
		Example     string           `json:"example"`
		Origin      string           `json:"origin"`
		Explanation string           `json:"explanation"`
		Examples    []GrammarExample `json:"examples"`
		Practice    []GrammarTask    `json:"practice"`
	} `json:"grammar"`
	Exercises []struct {
		Type        string          `json:"type"`
		Title       string          `json:"title"`
		Instruction string          `json:"instruction"`
		Payload     json.RawMessage `json:"payload"`
		AnswerKey   json.RawMessage `json:"answer_key"`
		RawSource   string          `json:"raw_source"`
	} `json:"exercises"`
	Avatar *avatarInput `json:"avatar"`
	// Цели присылает только свой экран: nil значит «не трогать».
	Objectives *[]struct {
		Text string `json:"text"`
	} `json:"objectives"`
}

// ParseDraft разбирает урок в том же JSON, что и тело создания урока:
// title, lang, level, refined_text, lexicon, objectives, avatar и т. д.
// Неизвестное поле — ошибка: опечатка в файле урока не должна молча
// превращаться в урок без целей.
func ParseDraft(raw []byte) (DraftContent, error) {
	var in draftInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return DraftContent{}, fmt.Errorf("content: не удалось разобрать урок: %w", err)
	}
	return in.toDraft(), nil
}

func (in draftInput) toDraft() DraftContent {
	d := DraftContent{
		PreserveContentIDs: in.PreserveContentIDs,
		QualityWarnings:    in.QualityWarnings,
		Title:              in.Title, LangCode: in.Lang, Level: in.Level,
		SourceKind: in.SourceKind, RefinedText: in.RefinedText,
		FrankText: in.FrankText, GrammarRaw: in.GrammarRaw, Note: in.Note,
	}
	for _, u := range in.Lexicon {
		d.Lexicon = append(d.Lexicon, LexicalUnit{
			Phrase: u.Phrase, Translation: u.Translation, Origin: u.Origin,
		})
	}
	for _, g := range in.Grammar {
		d.Grammar = append(d.Grammar, GrammarTopic{
			Title: g.Title, Example: g.Example, Origin: g.Origin,
			Explanation: g.Explanation, Examples: g.Examples, Practice: g.Practice,
		})
	}
	for _, e := range in.Exercises {
		d.Exercises = append(d.Exercises, Exercise{
			Type: e.Type, Title: e.Title, Instruction: e.Instruction,
			Payload: e.Payload, AnswerKey: e.AnswerKey, RawSource: e.RawSource,
		})
	}
	if in.Objectives != nil {
		d.Objectives = make([]Objective, 0, len(*in.Objectives))
		for _, o := range *in.Objectives {
			d.Objectives = append(d.Objectives, Objective{Text: o.Text})
		}
	}
	if a := in.Avatar; a != nil {
		enabled := true
		if a.Enabled != nil {
			enabled = *a.Enabled
		}
		d.Avatar = &AvatarConfig{
			Enabled: enabled, RolePrompt: a.RolePrompt, MaxTurns: a.MaxTurns,
			PracticePhrases: a.PracticePhrases, PracticeGrammar: a.PracticeGrammar,
			PracticePhraseNotes:  notesIn(a.PracticePhraseNotes),
			PracticeGrammarNotes: notesIn(a.PracticeGrammarNotes),
			HintEnabled:          a.HintEnabled, DictionaryEnabled: a.DictionaryEnabled,
			Saved: a.Saved,
		}
	}
	return d
}

// notesIn — пояснения из тела правки. nil — поля не было: сервис оставит
// прежние пояснения. Присланный пустой список остаётся пустым, а не nil.
func notesIn(notes *[]string) []string {
	if notes == nil {
		return nil
	}
	return append(make([]string, 0, len(*notes)), (*notes)...)
}

// notesPtr — пояснения для ответа учителю: всегда массив той же длины, что
// список целей.
func notesPtr(items, notes []string) *[]string {
	out := alignNotes(items, notes)
	return &out
}

// ---------------------------------------------------------------------------
// Обработчики
// ---------------------------------------------------------------------------

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	rows, err := a.svc.LibraryView(r.Context(), iam.MustActor(r.Context()), LessonFilter{
		Level:    q.Get("level"),
		LangCode: q.Get("lang"),
		Status:   q.Get("status"),
		Query:    q.Get("q"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]lessonDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, toLibraryDTO(row))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"lessons": out})
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "lessonID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	actor := iam.MustActor(r.Context())
	lesson, err := a.svc.Lesson(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	versions, err := a.svc.Versions(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]versionDTO, 0, len(versions))
	for _, v := range versions {
		out = append(out, toVersionDTO(v))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"lesson": toLessonDTO(lesson), "versions": out,
	})
}

func (a *API) archive(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "lessonID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		Archived bool `json:"archived"`
	}
	if err := httpx.DecodeJSON(r, &in, 1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lesson, err := a.svc.Archive(r.Context(), iam.MustActor(r.Context()), id, in.Archived)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"lesson": toLessonDTO(lesson)})
}

func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "lessonID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.Delete(r.Context(), iam.MustActor(r.Context()), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) versions(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "lessonID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, err := a.svc.Versions(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]versionDTO, 0, len(items))
	for _, v := range items {
		out = append(out, toVersionDTO(v))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (a *API) version(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "versionID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	c, err := a.svc.TeacherVersion(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toContentDTO(c))
}

func (a *API) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "versionID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		Status string `json:"status"`
		// Название и заметка приходят из формы публикации и только оттуда:
		// после публикации версия неизменяема, и записать их позже нельзя.
		Title *string `json:"title"`
		Note  *string `json:"note"`
	}
	if err := httpx.DecodeJSON(r, &in, 1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	version, err := a.svc.SetVersionStatus(r.Context(), iam.MustActor(r.Context()), id, in.Status, in.Title, in.Note)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": toVersionDTO(version)})
}

// pathUUID разбирает идентификатор из пути. Мусор — 404, а не 400:
// клиент не должен по коду ответа отличать «неверный формат» от «чужое».
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, httpx.NotFound("Не найдено")
	}
	return id, nil
}
