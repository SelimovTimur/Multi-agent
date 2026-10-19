// Package arch проверяет архитектурные правила, которые нельзя выразить
// компилятором.
//
// Эти тесты — не про поведение кода, а про то, чтобы модульная структура не
// растворилась через полгода. Нарушение видно на CI сразу, а не когда модуль
// уже нельзя вынести в отдельный сервис.
package arch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot — корень репозитория относительно этого пакета.
const repoRoot = "../.."

// schemaOwners — какой модуль какой схеме принадлежит.
//
// Правило: модуль читает и пишет ТОЛЬКО свои схемы. Данные чужого домена
// берутся через Go-интерфейс (см. iam.StudentDirectory), а не join'ом.
// Именно это позволяет вынести модуль в отдельный сервис, не переписывая
// запросы, и не даёт схемам срастись в неразделимый ком.
var schemaOwners = map[string][]string{
	"iam":       {"iam"},
	"classroom": {"classroom"},
	"content":   {"content"},
	"learning":  {"learning"},
	"lexicon":   {"lexicon"},

	// Инфраструктурные пакеты.
	"mlgw":       {"ops"},    // учёт вызовов моделей
	"live":       {"ops"},    // техническая наблюдаемость голосовых сессий
	"app":        {"public"}, // служебные функции (секции таблиц)
	"lessonpipe": {"ops"},    // учёт вызовов движка разговора

	// Журнал применённых миграций лежит в public: он про схему целиком,
	// а не про какой-то один модуль.
	"platform/migrate": {"public"},
}

// knownSchemas — все схемы базы. Из них собирается регулярка поиска.
var knownSchemas = []string{"iam", "classroom", "content", "learning", "ops", "lexicon", "public"}

var schemaRefRe = regexp.MustCompile(`\b(` + strings.Join(knownSchemas, "|") + `)\.[a-z_][a-z0-9_]*`)

// TestModuleSchemaBoundaries: ни один модуль не обращается к чужой схеме.
func TestModuleSchemaBoundaries(t *testing.T) {
	files := goFiles(t, filepath.Join(repoRoot, "internal"))

	for _, file := range files {
		module := moduleOf(file)
		if module == "" {
			continue
		}
		allowed := schemaOwners[module]
		if allowed == nil {
			// Пакет не владеет ни одной схемой — значит, SQL в нём быть не должно.
			allowed = []string{}
		}

		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("не удалось прочитать %s: %v", file, err)
		}

		for _, sql := range rawStrings(string(src)) {
			for _, ref := range schemaRefRe.FindAllStringSubmatch(sql, -1) {
				schema := ref[1]
				if contains(allowed, schema) {
					continue
				}
				t.Errorf(
					"%s: модуль %q обращается к чужой схеме %q (%s).\n"+
						"Данные чужого домена берутся через Go-интерфейс, а не SQL.\n"+
						"Если схема действительно принадлежит модулю — допишите её в schemaOwners.",
					rel(file), module, schema, ref[0])
			}
		}
	}
}

// forbiddenImports — модули домена не тянут за собой legacy-сервисы.
//
// internal/service — это старый контур (in-memory сессии, прямые вызовы
// моделей). Доменные модули нового контура ходят к моделям только через
// mlgw, иначе приоритеты пулов и учёт стоимости перестают работать.
var forbiddenImports = map[string][]string{
	"iam":       {"github.com/NATIVE/NATIVE/internal/service"},
	"classroom": {"github.com/NATIVE/NATIVE/internal/service"},
	"content":   {"github.com/NATIVE/NATIVE/internal/service"},
	"learning":  {"github.com/NATIVE/NATIVE/internal/service"},
	"mlgw":      {"github.com/NATIVE/NATIVE/internal/service"},

	// speech в этом списке нет намеренно: internal/service — это клиенты
	// ASR, ffmpeg и синтеза, и speech ими пользуется.
}

var importRe = regexp.MustCompile(`"(github\.com/NATIVE/NATIVE/[^"]+)"`)

func TestForbiddenImports(t *testing.T) {
	for _, file := range goFiles(t, filepath.Join(repoRoot, "internal")) {
		module := moduleOf(file)
		banned := forbiddenImports[module]
		if len(banned) == 0 {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("не удалось прочитать %s: %v", file, err)
		}
		for _, m := range importRe.FindAllStringSubmatch(string(src), -1) {
			if contains(banned, m[1]) {
				t.Errorf("%s: модуль %q импортирует %s — это запрещено.\n"+
					"К моделям ходим через internal/mlgw, к HTTP — через собственный http.go.",
					rel(file), module, m[1])
			}
		}
	}
}

// TestPlatformIsIndependent: платформенный слой не знает о доменах.
//
// internal/platform — фундамент (логи, ошибки, база, идентификаторы).
// Если он начнёт зависеть от iam или classroom, переиспользовать его в
// отдельном сервисе станет невозможно.
func TestPlatformIsIndependent(t *testing.T) {
	domains := []string{"/internal/iam", "/internal/classroom", "/internal/content",
		"/internal/learning", "/internal/mlgw", "/internal/lessonpipe", "/internal/app",
		"/internal/service", "/internal/speech", "/internal/live"}

	for _, file := range goFiles(t, filepath.Join(repoRoot, "internal", "platform")) {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("не удалось прочитать %s: %v", file, err)
		}
		for _, m := range importRe.FindAllStringSubmatch(string(src), -1) {
			for _, d := range domains {
				if strings.Contains("/"+m[1], d) {
					t.Errorf("%s: платформенный пакет импортирует домен %s.\n"+
						"Зависимость должна идти в обратную сторону.", rel(file), m[1])
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Вспомогательное
// ---------------------------------------------------------------------------

// moduleOf возвращает имя модуля по пути файла: internal/iam/store.go → "iam".
// Для internal/platform/... возвращает "platform/<пакет>".
func moduleOf(path string) string {
	norm := filepath.ToSlash(path)
	idx := strings.Index(norm, "/internal/")
	if idx < 0 {
		return ""
	}
	rest := norm[idx+len("/internal/"):]
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return ""
	}
	if parts[0] == "platform" && len(parts) >= 3 {
		return "platform/" + parts[1]
	}
	return parts[0]
}

func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("не удалось обойти %s: %v", root, err)
	}
	return out
}

// rawStrings достаёт содержимое строк в обратных кавычках — там живёт SQL.
// Обычные строки в кавычках не разбираем: в них SQL не пишут, а имена
// таблиц вида "iam" встречаются как обычные значения.
func rawStrings(src string) []string {
	var out []string
	for {
		start := strings.IndexByte(src, '`')
		if start < 0 {
			return out
		}
		src = src[start+1:]
		end := strings.IndexByte(src, '`')
		if end < 0 {
			return out
		}
		out = append(out, src[:end])
		src = src[end+1:]
	}
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func rel(path string) string {
	norm := filepath.ToSlash(path)
	if idx := strings.Index(norm, "internal/"); idx >= 0 {
		return norm[idx:]
	}
	return norm
}
