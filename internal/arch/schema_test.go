package arch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func migrationsDir() string { return filepath.Join(repoRoot, "migrations") }

func readMigration(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(migrationsDir(), name))
	if err != nil {
		t.Fatalf("не удалось прочитать миграцию %s: %v", name, err)
	}
	return string(data)
}

// TestEveryMigrationHasDown: у каждой миграции есть откат.
//
// Миграция без отката превращает неудачный выкат в ручной инцидент на
// проде в разгар уроков. Пара .up/.down обязательна с первого дня.
func TestEveryMigrationHasDown(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir())
	if err != nil {
		t.Fatalf("не удалось прочитать каталог миграций: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		down := strings.TrimSuffix(name, ".up.sql") + ".down.sql"
		if _, err := os.Stat(filepath.Join(migrationsDir(), down)); err != nil {
			t.Errorf("для миграции %s нет отката %s", name, down)
		}
	}
}

// Две ветки могут добавить разные миграции с одним номером. Движок
// индексирует их по версии и потеряет одну, поэтому проверяем слияния здесь.
func TestMigrationVersionsAreUnique(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir())
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		version := strings.SplitN(name, "_", 2)[0]
		if previous, exists := versions[version]; exists {
			t.Errorf("номер %s повторяется в миграциях %s и %s", version, previous, name)
		}
		versions[version] = name
	}
}

// TestLessonsHaveNoClassID: урок не привязан к классу.
//
// Урок создаётся один раз и назначается в разные классы и разные годы.
// class_id в content.lessons означал бы копию урока на каждый класс —
// и рассинхрон при первой же правке (NR-16).
func TestLessonsHaveNoClassID(t *testing.T) {
	sql := readMigration(t, "0001_schema.up.sql")
	table := tableBody(t, sql, "content.lessons")
	if strings.Contains(table, "class_id") {
		t.Errorf("в content.lessons появился class_id.\n"+
			"Урок не принадлежит классу: связь идёт через learning.assignments.\nТело таблицы:\n%s", table)
	}
}

// TestAssignmentsReferenceVersion: назначение выдаётся на версию урока.
//
// Ссылка на урок вместо версии означала бы, что правка урока меняет то, что
// уже видит класс. Ровно это NR-23 и запрещает: класс, получивший урок в
// сентябре, в мае должен видеть сентябрьскую редакцию.
func TestAssignmentsReferenceVersion(t *testing.T) {
	sql := readMigration(t, "0001_schema.up.sql")
	table := tableBody(t, sql, "learning.assignments")

	if !strings.Contains(table, "lesson_version_id") {
		t.Error("в learning.assignments нет lesson_version_id — назначение обязано ссылаться на версию")
	}
	// Именно "lesson_id" отдельным полем: без него правка урока задевала бы
	// уже выданное.
	for _, line := range strings.Split(table, "\n") {
		field := strings.TrimSpace(line)
		if strings.HasPrefix(field, "lesson_id ") || strings.HasPrefix(field, "lesson_id\t") {
			t.Error("в learning.assignments появился lesson_id: назначение должно ссылаться только на версию")
		}
	}
}

// personalDataColumns — поля, которых у ученика быть не должно (152-ФЗ).
//
// Ученик заходит по коду класса, а не по учётке. Поэтому у него нет ни ФИО,
// ни почты, ни телефона, ни возраста: есть только «как тебя показывать»,
// короткий тег для различения тёзок и персональный код возврата.
var personalDataColumns = []string{
	"first_name", "last_name", "middle_name", "full_name", "surname", "patronymic",
	"email", "phone", "birth", "age", "passport", "snils", "address",
}

func TestStudentsHaveNoPersonalData(t *testing.T) {
	sql := readMigration(t, "0001_schema.up.sql")
	table := tableBody(t, sql, "iam.students")
	for _, col := range personalDataColumns {
		if strings.Contains(table, col) {
			t.Errorf("в iam.students появилось поле с персональными данными: %q.\n"+
				"У ученика по 152-ФЗ нет ФИО, почты, телефона и возраста.\n"+
				"Если поле действительно нужно — это отдельное решение с юристом,\n"+
				"обновлением docs/personal-data.md и согласием родителей.", col)
		}
	}
}

// TestEventTablesArePartitioned: таблицы событий секционированы с первого дня.
//
// answer_events, dialog_turns и pipeline_calls растут быстрее всех остальных.
// Добавить секционирование на живой таблице в 300 млн строк — это часы
// простоя, поэтому оно заводится сразу, пока таблицы пустые (И-5).
func TestEventTablesArePartitioned(t *testing.T) {
	cases := []struct{ file, table string }{
		{"0001_schema.up.sql", "learning.answer_events"},
		{"0001_schema.up.sql", "learning.dialog_turns"},
		{"0001_schema.up.sql", "ops.pipeline_calls"},
	}
	for _, c := range cases {
		sql := readMigration(t, c.file)
		re := regexp.MustCompile(`(?is)CREATE TABLE ` + regexp.QuoteMeta(c.table) + `\s*\(.*?\)\s*PARTITION BY RANGE`)
		if !re.MatchString(sql) {
			t.Errorf("таблица %s должна быть секционирована по времени (PARTITION BY RANGE)", c.table)
		}
	}
}

// TestTenantColumnEverywhere: у прикладных таблиц есть org_id.
//
// Мультитенантность заводится с первого дня: дописать org_id в живые
// таблицы позже — это переливка данных и ревизия каждого запроса.
func TestTenantColumnEverywhere(t *testing.T) {
	// Справочники и служебные таблицы тенанту не принадлежат.
	exempt := map[string]bool{
		"lexicon.languages":        true,
		"lexicon.levels":           true,
		"iam.organizations":        true, // сама таблица тенантов
		"iam.roles":                true,
		"iam.permissions":          true,
		"iam.role_permissions":     true,
		"iam.login_attempts":       true, // антифрод, до определения тенанта
		"public.schema_migrations": true,
		"ops.telemetry_instances":  true, // аренда процесса API объединяет все организации
	}

	re := regexp.MustCompile(`(?is)CREATE TABLE (\w+\.\w+)\s*\((.*?)\n\)`)
	entries, err := os.ReadDir(migrationsDir())
	if err != nil {
		t.Fatalf("не удалось прочитать каталог миграций: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		sql := readMigration(t, e.Name())
		for _, m := range re.FindAllStringSubmatch(sql, -1) {
			name, body := m[1], m[2]
			if exempt[name] || strings.Contains(name, "_20") {
				continue
			}
			if !strings.Contains(body, "org_id") {
				t.Errorf("%s: в таблице %s нет org_id.\n"+
					"Каждая прикладная строка принадлежит организации.\n"+
					"Если таблица действительно общая — добавьте её в exempt в этом тесте.",
					e.Name(), name)
			}
		}
	}
}

// tableBody вырезает тело CREATE TABLE для указанной таблицы.
func tableBody(t *testing.T, sql, table string) string {
	t.Helper()
	re := regexp.MustCompile(`(?is)CREATE TABLE ` + regexp.QuoteMeta(table) + `\s*\((.*?)\n\)`)
	m := re.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("не найдено определение таблицы %s", table)
	}
	return m[1]
}
