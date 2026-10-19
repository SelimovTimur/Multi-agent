package content

// Настройка разговора с аватаром (NR-29).
//
// Настройка — часть версии урока, как лексика и упражнения: опубликованная
// версия не меняется, правка уходит в новую. Триггера на avatar_configs в
// базе нет, поэтому неизменяемость держится здесь, в EditVersion.

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

const avatarColumns = `enabled, role_prompt, max_turns, practice_phrases, practice_grammar,
	practice_phrase_notes, practice_grammar_notes, hint_enabled, dictionary_enabled, configured`

func scanAvatar(row pg.Row) (AvatarConfig, error) {
	var c AvatarConfig
	err := row.Scan(&c.Enabled, &c.RolePrompt, &c.MaxTurns, &c.PracticePhrases,
		&c.PracticeGrammar, &c.PracticePhraseNotes, &c.PracticeGrammarNotes,
		&c.HintEnabled, &c.DictionaryEnabled, &c.Saved)
	// Настройки, сохранённые до пояснений, хранят пустые массивы: дальше
	// код рассчитывает на пояснение у каждой цели, поэтому выравниваем здесь,
	// один раз при чтении.
	c.PracticePhraseNotes = alignNotes(c.PracticePhrases, c.PracticePhraseNotes)
	c.PracticeGrammarNotes = alignNotes(c.PracticeGrammar, c.PracticeGrammarNotes)
	return c, err
}

// AvatarConfig — настройка аватара версии. Без сохранённой настройки
// возвращает умолчание: разговор в уроке есть и идёт базово.
func (s *Store) AvatarConfig(ctx context.Context, q pg.Querier, orgID, versionID uuid.UUID) (AvatarConfig, error) {
	c, err := scanAvatar(q.QueryRow(ctx, `
		SELECT `+avatarColumns+` FROM content.avatar_configs
		WHERE org_id = $1 AND lesson_version_id = $2`, orgID, versionID))
	if pg.IsNoRows(err) {
		return DefaultAvatarConfig(), nil
	}
	if err != nil {
		return AvatarConfig{}, fmt.Errorf("content: не удалось прочитать настройку аватара: %w", err)
	}
	return c, nil
}

// LatestLessonAvatarConfig — настройка из самой свежей версии урока, где
// учитель её сохранял. Нужна новой версии: правка текста не должна молча
// сбрасывать роль аватара.
func (s *Store) LatestLessonAvatarConfig(ctx context.Context, q pg.Querier, orgID,
	lessonID uuid.UUID) (AvatarConfig, bool, error) {
	c, err := scanAvatar(q.QueryRow(ctx, `
		SELECT a.enabled, a.role_prompt, a.max_turns, a.practice_phrases, a.practice_grammar,
		       a.practice_phrase_notes, a.practice_grammar_notes,
		       a.hint_enabled, a.dictionary_enabled, a.configured
		FROM content.avatar_configs a
		JOIN content.lesson_versions v ON v.id = a.lesson_version_id
		WHERE v.org_id = $1 AND v.lesson_id = $2
		ORDER BY v.version DESC LIMIT 1`, orgID, lessonID))
	if pg.IsNoRows(err) {
		return AvatarConfig{}, false, nil
	}
	if err != nil {
		return AvatarConfig{}, false, fmt.Errorf("content: не удалось прочитать настройку аватара: %w", err)
	}
	return c, true, nil
}

func (s *Store) UpsertAvatarConfig(ctx context.Context, q pg.Querier, orgID, versionID uuid.UUID,
	c AvatarConfig) error {
	// Пояснения пишутся выровненными по спискам: пустые массивы рядом с
	// непустыми списками — признак старой записи, а не «пояснений нет».
	_, err := q.Exec(ctx, `
		INSERT INTO content.avatar_configs
			(lesson_version_id, org_id, enabled, role_prompt, max_turns,
			 practice_phrases, practice_grammar, practice_phrase_notes, practice_grammar_notes,
			 hint_enabled, dictionary_enabled, configured)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (lesson_version_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			role_prompt = EXCLUDED.role_prompt,
			max_turns = EXCLUDED.max_turns,
			practice_phrases = EXCLUDED.practice_phrases,
			practice_grammar = EXCLUDED.practice_grammar,
			practice_phrase_notes = EXCLUDED.practice_phrase_notes,
			practice_grammar_notes = EXCLUDED.practice_grammar_notes,
			hint_enabled = EXCLUDED.hint_enabled,
			dictionary_enabled = EXCLUDED.dictionary_enabled,
			-- Однажды настроенный разговор остаётся настроенным: выключение
			-- блока с карты не превращает правку учителя в умолчание.
			configured = content.avatar_configs.configured OR EXCLUDED.configured`,
		versionID, orgID, c.Enabled, c.RolePrompt, c.MaxTurns,
		nonNil(c.PracticePhrases), nonNil(c.PracticeGrammar),
		alignNotes(c.PracticePhrases, c.PracticePhraseNotes),
		alignNotes(c.PracticeGrammar, c.PracticeGrammarNotes),
		c.HintEnabled, c.DictionaryEnabled, c.Saved)
	if err != nil {
		return fmt.Errorf("content: не удалось сохранить настройку аватара: %w", err)
	}
	return nil
}

func nonNilExamples(items []GrammarExample) []GrammarExample {
	if items == nil {
		return []GrammarExample{}
	}
	return items
}

func nonNilTasks(items []GrammarTask) []GrammarTask {
	if items == nil {
		return []GrammarTask{}
	}
	return items
}

func nonNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// promptInjectionMarkers — попытки учительского текста отменить правила
// аватара. Список тот же по смыслу, что в llm_service/app/validators.py, но
// без «притворись» и «act as»: роль продавца на рынке — ровно то, ради чего
// поле существует. Проверка в llm_service остаётся второй линией.
var promptInjectionMarkers = []string{
	"ignore previous", "ignore all previous", "ignore the above", "disregard previous",
	"system prompt", "developer mode", "jailbreak", "reveal your instructions",
	"игнорируй", "проигнорируй", "забудь инструкции", "забудь все инструкции",
	"забудь предыдущие", "системный промпт", "системные инструкции",
	"покажи инструкции", "раскрой инструкции", "отмени правила", "без ограничений",
}

// LooksLikePromptInjection — текст пытается переписать правила аватара.
//
// Перед сравнением текст нормализуется: «И г н о р и р у й», неразрывные
// пробелы и разный регистр — это тот же маркер, а подстрока в лоб их не
// ловит. Проверка остаётся грубой (список подстрок), поэтому вторая линия
// стоит в llm_service, а сам текст уходит в промпт как данные за
// ограждением, а не как инструкция.
func LooksLikePromptInjection(text string) bool {
	low := normalizeForMarkers(text)
	for _, m := range promptInjectionMarkers {
		if strings.Contains(low, strings.ReplaceAll(m, " ", "")) {
			return true
		}
	}
	// Ограждение чужого текста в промпте: в самом тексте ему делать нечего.
	return strings.Contains(text, "<<<") || strings.Contains(text, ">>>")
}

// normalizeForMarkers приводит текст к виду, в котором маркеры не спрятать
// пробелами, регистром и невидимыми символами.
func normalizeForMarkers(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsSpace(r), r == '\u00a0', r == '\u200b', r == '\u2060':
			continue
		case unicode.IsPunct(r) && r != '-':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeAvatar проверяет настройку и приводит её к сохраняемому виду.
func normalizeAvatar(c AvatarConfig, fields map[string]string) AvatarConfig {
	c.RolePrompt = strings.TrimSpace(c.RolePrompt)
	switch {
	case len([]rune(c.RolePrompt)) > AvatarRoleMaxRunes:
		fields["avatar.role_prompt"] = fmt.Sprintf("Роль и тон — не длиннее %d символов", AvatarRoleMaxRunes)
	case LooksLikePromptInjection(c.RolePrompt):
		fields["avatar.role_prompt"] = "Опишите роль и тон собеседника — менять правила аватара нельзя"
	}

	if c.MaxTurns == 0 {
		c.MaxTurns = AvatarDefaultTurns
	}
	if c.MaxTurns < AvatarMinTurns || c.MaxTurns > AvatarMaxTurns {
		fields["avatar.max_turns"] = fmt.Sprintf("Длина разговора — от %d до %d реплик", AvatarMinTurns, AvatarMaxTurns)
	}

	c.PracticePhrases, c.PracticePhraseNotes = cleanGoals(c.PracticePhrases, c.PracticePhraseNotes,
		"avatar.practice_phrase_notes", "выражений", fields)
	if len(c.PracticePhrases) > AvatarMaxPhrases {
		fields["avatar.practice_phrases"] = fmt.Sprintf("Не больше %d выражений для разговора", AvatarMaxPhrases)
	}
	c.PracticeGrammar, c.PracticeGrammarNotes = cleanGoals(c.PracticeGrammar, c.PracticeGrammarNotes,
		"avatar.practice_grammar_notes", "конструкций", fields)
	if len(c.PracticeGrammar) > AvatarMaxGrammar {
		fields["avatar.practice_grammar"] = fmt.Sprintf("Не больше %d конструкций для разговора", AvatarMaxGrammar)
	}
	for _, it := range append(append([]string{}, c.PracticePhrases...), c.PracticeGrammar...) {
		if len([]rune(it)) > AvatarItemMaxRunes {
			fields["avatar.practice_phrases"] = fmt.Sprintf("Выражение не длиннее %d символов", AvatarItemMaxRunes)
			break
		}
		if LooksLikePromptInjection(it) {
			fields["avatar.practice_phrases"] = "Выражения для разговора не должны менять правила аватара"
			break
		}
	}
	checkNotes(c.PracticePhraseNotes, "avatar.practice_phrase_notes", fields)
	checkNotes(c.PracticeGrammarNotes, "avatar.practice_grammar_notes", fields)
	return c
}

// cleanGoals чистит список целей вместе с пояснениями: пустая цель и повтор
// уходят из списка со своим пояснением, иначе пояснение съехало бы к
// соседней цели. notes == nil — пояснений не прислали, и nil остаётся nil:
// это сигнал сервису взять их у прежней настройки.
//
// Длина сверяется до чистки, по тому, что прислал экран: пояснение
// относится к строке экрана, а не к её месту после удаления пустых.
func cleanGoals(items, notes []string, field, what string, fields map[string]string) ([]string, []string) {
	if notes != nil && len(notes) != len(items) {
		fields[field] = "Пояснений должно быть столько же, сколько " + what
		notes = nil
	}
	outItems := make([]string, 0, len(items))
	var outNotes []string
	if notes != nil {
		outNotes = make([]string, 0, len(items))
	}
	seen := map[string]bool{}
	for i, it := range items {
		it = strings.TrimSpace(it)
		key := strings.ToLower(it)
		if it == "" || seen[key] {
			continue
		}
		seen[key] = true
		outItems = append(outItems, it)
		if notes != nil {
			outNotes = append(outNotes, strings.TrimSpace(notes[i]))
		}
	}
	return outItems, outNotes
}

// checkNotes — пояснения уходят в промпт разговора так же, как сами цели,
// поэтому и проверка та же: длина и попытка переписать правила аватара.
func checkNotes(notes []string, field string, fields map[string]string) {
	for _, n := range notes {
		if len([]rune(n)) > AvatarNoteMaxRunes {
			fields[field] = fmt.Sprintf("Пояснение — не длиннее %d символов", AvatarNoteMaxRunes)
			return
		}
		if LooksLikePromptInjection(n) {
			fields[field] = "Пояснения не должны менять правила аватара"
			return
		}
	}
}

// alignNotes дополняет пояснения пустыми строками до длины списка целей и
// отрезает лишние: у каждой цели ровно одно пояснение.
func alignNotes(items, notes []string) []string {
	out := make([]string, len(items))
	copy(out, notes)
	return out
}

// keepNotes переносит пояснения из прежней настройки, если правка их не
// прислала: старый клиент и карта урока о пояснениях не знают, и стирать
// написанное учителем из-за переключения блока нельзя.
//
// Пояснение переносится к той же цели, найденной по тексту без регистра:
// список тот же — пояснения те же; учитель поправил одну фразу — у неё
// пояснения нет, у остальных оно на месте.
func keepNotes(next *AvatarConfig, prev AvatarConfig) {
	if next.PracticePhraseNotes == nil {
		next.PracticePhraseNotes = notesByGoal(next.PracticePhrases, prev.PracticePhrases, prev.PracticePhraseNotes)
	}
	if next.PracticeGrammarNotes == nil {
		next.PracticeGrammarNotes = notesByGoal(next.PracticeGrammar, prev.PracticeGrammar, prev.PracticeGrammarNotes)
	}
}

// notesMissing — правка пришла без пояснений хотя бы к одному из списков.
func notesMissing(c *AvatarConfig) bool {
	return c != nil && (c.PracticePhraseNotes == nil || c.PracticeGrammarNotes == nil)
}

func notesByGoal(items, prevItems, prevNotes []string) []string {
	byGoal := make(map[string]string, len(prevItems))
	for i, it := range prevItems {
		if i < len(prevNotes) && prevNotes[i] != "" {
			byGoal[strings.ToLower(strings.TrimSpace(it))] = prevNotes[i]
		}
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = byGoal[strings.ToLower(strings.TrimSpace(it))]
	}
	return out
}
