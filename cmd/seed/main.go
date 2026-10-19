// Команда seed заводит демо-данные MVP: учителя, класс с учениками,
// готовые английские уроки и одну выданную домашнюю работу с разговором.
//
// Идемпотентна: повторный запуск ничего не дублирует и только печатает,
// как войти. В Docker она запускается после миграций при каждом старте.
//
//	go run ./cmd/seed
//
// Учётка учителя — из SEED_TEACHER_EMAIL и SEED_TEACHER_PASSWORD
// (умолчания — в README.md).
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/app"
	"github.com/NATIVE/NATIVE/internal/classroom"
	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/content"
	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/learning"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/google/uuid"
)

//go:embed lessons/*.json
var lessonFiles embed.FS

const (
	defaultEmail    = "teacher@example.com"
	defaultPassword = "english-demo-2026"
	className       = "7А"
	classSubject    = "Английский язык"
	classLevel      = "A2"
	// Урок, который сразу выдан классу на дом: ученик входит и говорит с
	// аватаром без участия учителя.
	homeworkLesson = "A school trip to London"
)

var students = []string{"Аня Смирнова", "Борис Петров", "Вика Ли"}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		// Не через log: после slog.SetDefault он пишет в slog уровнем info,
		// а уровень логгера здесь warn, и причина отказа пропала бы.
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	cfg := config.Load()
	plat := config.LoadPlatform()
	logger := logx.New("warn", plat.LogFormat)
	slog.SetDefault(logger)
	if !plat.Enabled() {
		return fmt.Errorf("не задан DATABASE_URL")
	}

	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	a, err := app.New(startCtx, plat, cfg, logger)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = a.Close(closeCtx)
	}()

	email := envOr("SEED_TEACHER_EMAIL", defaultEmail)
	password := envOr("SEED_TEACHER_PASSWORD", defaultPassword)
	teacher, created, err := a.IAM.EnsureTeacher(ctx, email, password, "Учитель английского", "Демо-школа")
	if err != nil {
		return fmt.Errorf("учитель: %w", err)
	}
	if !created {
		password = "(задан при первом запуске)"
	}

	lessons, err := ensureLessons(ctx, a.Content, teacher)
	if err != nil {
		return err
	}
	class, members, err := ensureClass(ctx, a.Classes, teacher)
	if err != nil {
		return err
	}
	if err := ensureHomework(ctx, a.Learn, teacher, class.ID, lessons[homeworkLesson]); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nДемо-данные готовы.\n\nУчитель: %s / %s\n", email, password)
	fmt.Fprintf(out, "Класс %s — коды учеников для входа:\n", class.Title)
	for _, m := range members {
		fmt.Fprintf(out, "  %-16s %s\n", m.DisplayName, m.PublicCode)
	}
	fmt.Fprintf(out, "Уроков в библиотеке: %d. Урок «%s» выдан классу на дом.\n\n", len(lessons), homeworkLesson)
	return nil
}

// ensureLessons заводит уроки из lessons/*.json и публикует их. Урок
// узнаётся по названию: уже заведённый не трогается.
func ensureLessons(ctx context.Context, svc *content.Service, teacher iam.Actor) (map[string]uuid.UUID, error) {
	rows, err := svc.LibraryView(ctx, teacher, content.LessonFilter{Limit: 200})
	if err != nil {
		return nil, fmt.Errorf("библиотека: %w", err)
	}
	published := map[string]uuid.UUID{}
	for _, r := range rows {
		if r.Published.Version == 0 {
			continue
		}
		v, err := svc.LatestPublished(ctx, teacher, r.Lesson.ID)
		if err != nil {
			return nil, fmt.Errorf("урок «%s»: %w", r.Lesson.Title, err)
		}
		published[r.Lesson.Title] = v.ID
	}

	names, err := lessonFiles.ReadDir("lessons")
	if err != nil {
		return nil, err
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	for _, f := range names {
		raw, err := lessonFiles.ReadFile(path.Join("lessons", f.Name()))
		if err != nil {
			return nil, err
		}
		draft, err := content.ParseDraft(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name(), err)
		}
		if _, ok := published[draft.Title]; ok {
			continue
		}
		_, version, err := svc.CreateFromDraft(ctx, teacher, draft)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name(), err)
		}
		for _, status := range []string{content.VersionReady, content.VersionPublished} {
			if _, err := svc.SetVersionStatus(ctx, teacher, version.ID, status, nil, nil); err != nil {
				return nil, fmt.Errorf("%s: публикация: %w", f.Name(), err)
			}
		}
		published[draft.Title] = version.ID
	}
	return published, nil
}

// ensureClass заводит класс и учеников, если у учителя ещё нет класса с
// таким названием, и отдаёт состав вместе с кодами входа.
func ensureClass(ctx context.Context, svc *classroom.Service, teacher iam.Actor) (classroom.Class, []classroom.Member, error) {
	classes, err := svc.ListClasses(ctx, teacher, false)
	if err != nil {
		return classroom.Class{}, nil, fmt.Errorf("классы: %w", err)
	}
	var class classroom.Class
	for _, c := range classes {
		if strings.EqualFold(c.Class.Title, className) {
			class = c.Class
		}
	}
	if class.ID == uuid.Nil {
		class, err = svc.CreateClass(ctx, teacher, classroom.ClassInput{
			Title: className, Subject: classSubject, LangCode: "english", Level: classLevel,
		})
		if err != nil {
			return classroom.Class{}, nil, fmt.Errorf("класс: %w", err)
		}
	}
	// Повторный список ничего не дублирует: имена, которые в классе уже
	// есть, пропускаются.
	if _, err := svc.AddStudents(ctx, teacher, class.ID, students); err != nil {
		return classroom.Class{}, nil, fmt.Errorf("ученики: %w", err)
	}
	members, err := svc.Roster(ctx, teacher, class.ID, false)
	if err != nil {
		return classroom.Class{}, nil, fmt.Errorf("состав класса: %w", err)
	}
	return class, members, nil
}

// ensureHomework назначает урок классу и выдаёт его на дом на неделю, если
// урок ещё не назначен.
func ensureHomework(ctx context.Context, svc *learning.Service, teacher iam.Actor, classID, versionID uuid.UUID) error {
	if versionID == uuid.Nil {
		return fmt.Errorf("урок «%s» не найден среди уроков seed", homeworkLesson)
	}
	views, err := svc.ClassAssignments(ctx, teacher, classID, "")
	if err != nil {
		return fmt.Errorf("уроки класса: %w", err)
	}
	for _, v := range views {
		if v.Assignment.LessonVersionID == versionID {
			return nil
		}
	}
	assignment, err := svc.Assign(ctx, teacher, learning.AssignInput{LessonVersionID: versionID, ClassID: classID})
	if err != nil {
		return fmt.Errorf("назначение: %w", err)
	}
	due := time.Now().In(svc.Zone()).AddDate(0, 0, 7).Format("2006-01-02")
	_, err = svc.IssueTasks(ctx, teacher, assignment.ID, learning.IssueInput{
		Kind:     learning.IssueHome,
		TaskKeys: []string{learning.TaskObjectives, learning.TaskText, learning.TaskLexicon, learning.TaskDialog},
		DueDate:  due,
	})
	if err != nil {
		return fmt.Errorf("выдача на дом: %w", err)
	}
	return nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
