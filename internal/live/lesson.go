package live

// Базовая политика голосового разговора.
//
// Политика лежит в session.json и одна на всех: язык, тон, делегирование,
// запрет веб-поиска. Урок её не переписывает, а дополняет (conversation.go) —
// ролью от учителя, целями и длиной разговора. Иначе настройка учителя могла
// бы снять языковые правила, а это ровно то, ради чего они там стоят.

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	envsettings "github.com/NATIVE/NATIVE/internal/platform/settings"
)

// lessonTextLimit — сколько текста урока уходит в инструкцию сессии.
const lessonTextLimit = 4000

// levelGuidanceLimit — сколько указаний уровня уходит в инструкцию сессии.
// Справочник отдаёт компактный текст, но инструкция идёт в каждый ответ
// голосовой модели, и потолок держит её размер предсказуемым.
const levelGuidanceLimit = 3000

// SupportedLanguage — язык, на котором собеседница говорит в session.json.
// Уроки других языков голосовой разговор не поддерживает: политика и голос
// заданы под английский.
const SupportedLanguage = "english"

// Supports — можно ли вести голосовой разговор на этом языке.
func (a *API) Supports(language string) bool {
	return a.key != "" && strings.EqualFold(strings.TrimSpace(language), SupportedLanguage)
}

// VoiceEnv — переменная окружения с голосом собеседницы.
//
// Голос лежит в session.json, а тот вшит в бинарник, и смена голоса стоила бы
// пересборки. Голос подбирается на слух, перебором, и переменная сводит круг
// «сменить — послушать» к перезапуску. Пустое значение оставляет голос из
// session.json.
const VoiceEnv = "LIVE_VOICE"

// voiceName — разрешённые имена голосов. Имя уходит в запрос к провайдеру,
// и опечатка вроде пробела или кавычки там даст отказ на ровном месте.
var voiceName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// withVoice подменяет голос значением из окружения.
//
// Ошибка разбора и негодное имя оставляют политику нетронутой: голос —
// настройка для подбора, и ронять из-за неё голосовой разговор незачем.
func withVoice(config json.RawMessage) json.RawMessage {
	name := strings.TrimSpace(os.Getenv(VoiceEnv))
	if name == "" || !voiceName.MatchString(name) {
		return config
	}
	var session map[string]any
	if err := json.Unmarshal(config, &session); err != nil {
		return config
	}
	audio, ok := session["audio"].(map[string]any)
	if !ok {
		return config
	}
	output, ok := audio["output"].(map[string]any)
	if !ok {
		return config
	}
	output["voice"] = name
	out, err := json.Marshal(session)
	if err != nil {
		return config
	}
	return out
}

// sessionPolicy — базовые настройки сессии с переопределениями модели и
// голоса из окружения.
func sessionPolicy() (json.RawMessage, error) {
	return withVoice(withModels(sessionConfig)), nil
}

// withModels применяет переопределения, не меняя инструкции и инструменты.
func withModels(config json.RawMessage) json.RawMessage {
	var session map[string]any
	if json.Unmarshal(config, &session) != nil {
		return config
	}
	backend := session["delegation"].(map[string]any)["responses"].(map[string]any)
	reasoning := backend["reasoning"].(map[string]any)
	model, helper, effort := envsettings.String("LIVE_MODEL"), envsettings.String("LIVE_BACKEND_MODEL"), envsettings.String("LIVE_REASONING_EFFORT")
	if session["model"] == model && backend["model"] == helper && reasoning["effort"] == effort {
		return config
	}
	session["model"], backend["model"], reasoning["effort"] = model, helper, effort
	out, err := json.Marshal(session)
	if err != nil {
		return config
	}
	return out
}

func appendInstruction(base any, extra string) string {
	text, _ := base.(string)
	if text == "" {
		return extra
	}
	return text + "\n\n" + extra
}

// fence — ограждение вокруг чужого текста в инструкции сессии.
//
// Внутри текста его быть не должно: иначе учитель или материал урока
// закрывают ограждение своей строкой и продолжают писать уже «от имени»
// инструкции. Символы редкие, потеря их в тексте урока ничего не значит.
func fence(text string) string {
	text = strings.ReplaceAll(text, "<<<", "«")
	text = strings.ReplaceAll(text, ">>>", "»")
	return strings.TrimSpace(text)
}

func clean(items []string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it = fence(it); it != "" {
			out = append(out, it)
		}
	}
	return out
}

// truncate режет по границе слова: обрывок фразы в промпте читается как
// оборванная мысль и сбивает модель сильнее, чем отсутствие хвоста.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	cut := string(runes[:limit])
	if i := strings.LastIndexAny(cut, " \n"); i > limit/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}
