package learning

import (
	"context"
	"errors"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StartLesson открывает урок ученику и возвращает его попытку.
//
// Повторный вызов возвращает начатую попытку, а не создаёт новую: ученик,
// у которого сел интернет, продолжает с того же места.
func (s *Service) StartLesson(ctx context.Context, actor iam.Actor, assignmentID uuid.UUID) (Attempt, VersionInfo, error) {
	a, err := s.studentAssignment(ctx, actor, assignmentID)
	if err != nil {
		return Attempt{}, VersionInfo{}, err
	}
	// Урок открывается, когда по нему выдано хоть одно задание. Какие именно
	// задания можно делать, решают выдачи — каждое на своём шаге.
	issued, err := s.store.HasIssues(ctx, s.pool, actor.OrgID, a.ID)
	if err != nil {
		return Attempt{}, VersionInfo{}, httpx.Internal("Не удалось открыть урок").Wrap(err)
	}
	if !issued {
		return Attempt{}, VersionInfo{}, httpx.Forbidden(msgNothingIssued)
	}

	versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, []uuid.UUID{a.LessonVersionID})
	if err != nil {
		return Attempt{}, VersionInfo{}, err
	}

	// Сколько в уроке вопросов, знает библиотека. Запоминаем при старте:
	// доля верных считается от этого числа, и вычислять его на каждом
	// показе аналитики значило бы читать урок ради одной цифры.
	questions, err := s.lessons.QuestionCount(ctx, actor.OrgID, a.LessonVersionID)
	if err != nil {
		return Attempt{}, VersionInfo{}, err
	}

	attempt, err := s.store.StartAttempt(ctx, s.pool, Attempt{
		ID: idgen.New(), OrgID: actor.OrgID, AssignmentID: a.ID, StudentID: actor.StudentID,
		TotalQuestions: questions,
	})
	if err != nil {
		return Attempt{}, VersionInfo{}, httpx.Internal("Не удалось открыть урок").Wrap(err)
	}
	telemetry.Emit(ctx, telemetry.Event{Name: "lesson_started", EntityID: attempt.ID, LessonID: versions[a.LessonVersionID].LessonID, AssignmentID: a.ID, ClassID: a.ClassID, OwnerID: a.CreatedBy})
	return attempt, versions[a.LessonVersionID], nil
}

// LessonContent отдаёт ученику содержимое урока по его попытке.
//
// Право доступа выводится из попытки, а не из прав на урок: у ученика прав
// из матрицы нет вовсе. Попытка доказывает, что урок ему выдан, — и она же
// перестаёт действовать, когда доступ закрывается.
//
// Ключ ответов сюда не попадает: content отдаёт его отдельным методом,
// который здесь не вызывается.
func (s *Service) LessonContent(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (any, error) {
	attempt, err := s.store.AttemptByID(ctx, s.pool, actor.OrgID, attemptID)
	if errors.Is(err, ErrNotFound) {
		return nil, httpx.NotFound("Урок не найден")
	}
	if err != nil {
		return nil, httpx.Internal("Не удалось открыть урок").Wrap(err)
	}
	if attempt.StudentID != actor.StudentID {
		return nil, httpx.NotFound("Урок не найден")
	}

	a, err := s.store.AssignmentByID(ctx, s.pool, actor.OrgID, attempt.AssignmentID)
	if err != nil {
		return nil, httpx.Internal("Не удалось открыть урок").Wrap(err)
	}
	// Содержимое отдаём, пока по уроку есть выдачи, — в том числе
	// закрытые: пройденное ученик перечитывает из «Завершённых». Делать
	// задания закрытой выдачи ему не дадут ответы и отметки — они
	// проверяются по выдаче.
	issued, err := s.store.HasIssues(ctx, s.pool, actor.OrgID, a.ID)
	if err != nil {
		return nil, httpx.Internal("Не удалось открыть урок").Wrap(err)
	}
	if !issued {
		return nil, httpx.Forbidden(msgNothingIssued)
	}

	return s.lessons.StudentContent(ctx, actor.OrgID, a.LessonVersionID)
}

// AnswerInput — ответ ученика. Проверяет его сервер.
type AnswerInput struct {
	AttemptID  uuid.UUID
	ExerciseID *uuid.UUID
	Step       int
	// ItemID — что именно отвечено: идентификатор вопроса в тесте или
	// левая часть пары в сопоставлении.
	ItemID string
	// Answer — выбранный вариант или введённый текст.
	Answer string
}

// AnswerResult — что вернуть ученику после ответа.
type AnswerResult struct {
	Attempt  Attempt
	Correct  bool
	Expected string
	// Repeated — на этот вопрос уже отвечали: счёт не изменился, ученику
	// показываем прежний результат.
	Repeated bool
}

// answerKey — чем один ответ отличается от другого внутри попытки.
//
// Упражнение плюс вопрос: в сопоставлении «вопрос» — это левая часть пары,
// в тестах — идентификатор вопроса. Без упражнения в ключе одинаковые
// номера вопросов из разных заданий слиплись бы в один.
func answerKey(exerciseID *uuid.UUID, itemID string) string {
	if exerciseID == nil {
		return "-:" + itemID
	}
	return exerciseID.String() + ":" + itemID
}

// AttemptState — состояние попытки ученика: счёт и прежние ответы.
func (s *Service) AttemptState(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (Attempt, error) {
	return s.ownAttempt(ctx, actor, attemptID)
}

// SetStep запоминает, до какого шага дошёл ученик.
//
// Отдельной ручкой, а не вместе с ответом: ученик листает урок и не отвечая,
// а поле отвечает на вопрос «где он остановился».
func (s *Service) SetStep(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, step int) error {
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return err
	}
	if step < 0 || step > 200 {
		return httpx.FieldError("step", "Неизвестный шаг урока")
	}
	if err := s.store.SetAttemptStep(ctx, s.pool, actor.OrgID, attempt.ID, step); err != nil {
		return httpx.Internal("Не удалось сохранить шаг").Wrap(err)
	}
	return nil
}

// RecordAnswer обновляет прогресс и пишет событие.
//
// Прогресс — синхронно: от него зависит, что ученик увидит следующим шагом.
// Событие — асинхронно: это аналитика, и её сбой не должен ронять урок
// (разрешение №4 в роадмапе).
func (s *Service) RecordAnswer(ctx context.Context, actor iam.Actor, in AnswerInput) (AnswerResult, error) {
	attempt, err := s.store.AttemptByID(ctx, s.pool, actor.OrgID, in.AttemptID)
	if errors.Is(err, ErrNotFound) {
		return AnswerResult{}, httpx.NotFound("Попытка не найдена")
	}
	if err != nil {
		return AnswerResult{}, httpx.Internal("Не удалось записать ответ").Wrap(err)
	}
	if attempt.StudentID != actor.StudentID {
		return AnswerResult{}, httpx.NotFound("Попытка не найдена")
	}

	// Ответ принимается, только пока задание открыто: выдачу завершили или
	// срок прошёл — уже записанные ответы остаются, новые не принимаются.
	a, b, err := s.attemptBoard(ctx, actor, attempt)
	if err != nil {
		return AnswerResult{}, err
	}
	var task LessonTask
	var issueID uuid.UUID
	if in.ExerciseID != nil {
		taskKey := ExerciseTaskKey(*in.ExerciseID)
		if _, ok := b.task(taskKey); !ok {
			return AnswerResult{}, httpx.NotFound("Упражнение не найдено")
		}
		if task, issueID, err = openTaskOf(b, taskKey); err != nil {
			return AnswerResult{}, err
		}
	} else if !b.anyOpen() {
		// Ответ без упражнения ни к какому заданию не относится: принимаем,
		// пока у урока открыто хоть что-то, как раньше принимали в открытом
		// уроке.
		return AnswerResult{}, httpx.Conflict(msgNotIssued)
	} else {
		for i, is := range b.issues {
			if b.open[i] {
				issueID = is.ID
				break
			}
		}
	}

	// Правильность решает сервер. Клиент ключа не видит и видеть не должен:
	// иначе ответы лежат в исходниках страницы, а «верно» можно прислать на
	// что угодно.
	check := AnswerCheck{}
	if in.ExerciseID != nil {
		check, err = s.lessons.CheckAnswer(ctx, actor.OrgID, a.LessonVersionID,
			*in.ExerciseID, in.ItemID, in.Answer)
		if err != nil {
			return AnswerResult{}, err
		}
	}

	// Завершённая попытка ответов не принимает, и на один вопрос отвечают
	// один раз. Иначе ученику достаточно открыть пройденный урок заново и
	// нажать тот же верный вариант, чтобы поднять себе долю верных.
	if attempt.FinishedAt != nil {
		return AnswerResult{}, httpx.Conflict("Урок уже завершён — пройди его заново, чтобы ответить снова")
	}
	key := answerKey(in.ExerciseID, in.ItemID)
	if given, ok := attempt.Answers[key]; ok {
		return AnswerResult{
			Attempt: attempt, Correct: given.Correct, Expected: given.Expected, Repeated: true,
		}, nil
	}

	given := GivenAnswer{Answer: in.Answer, Correct: check.Correct, Expected: check.Expected, Step: in.Step,
		At: time.Now().UTC()}
	updated, err := s.saveAnswer(ctx, actor.OrgID, in, check, key, given, task, issueID, b.tasks)
	if errors.Is(err, ErrIssueGone) {
		// Учитель отменил выдачу, пока ответ шёл: задания у класса больше нет,
		// и ответ не записан вместе со всем остальным.
		return AnswerResult{}, httpx.Conflict(msgNotIssued)
	}
	if errors.Is(err, ErrNotFound) {
		// Между чтением попытки и записью успел пройти такой же ответ из
		// соседней вкладки либо урок завершили. Перечитываем и отвечаем тем,
		// что уже записано, — второй раз счёт не двигаем.
		fresh, readErr := s.store.AttemptByID(ctx, s.pool, actor.OrgID, in.AttemptID)
		if readErr == nil {
			if given, ok := fresh.Answers[key]; ok {
				return AnswerResult{
					Attempt: fresh, Correct: given.Correct, Expected: given.Expected, Repeated: true,
				}, nil
			}
			if fresh.FinishedAt != nil {
				return AnswerResult{}, httpx.Conflict("Урок уже завершён — пройди его заново, чтобы ответить снова")
			}
		}
		return AnswerResult{}, httpx.Internal("Не удалось записать ответ").Wrap(err)
	}
	if err != nil {
		return AnswerResult{}, wrapErr(err, "Не удалось записать ответ")
	}

	telemetry.Emit(ctx, telemetry.Event{Name: "answer_submitted", EntityID: attempt.ID, Key: key, ClassID: a.ClassID, AssignmentID: a.ID, OwnerID: a.CreatedBy})
	if task.Key != "" {
		telemetry.Emit(ctx, telemetry.Event{Name: "task_started", EntityID: issueID, Key: task.Key, ClassID: a.ClassID, AssignmentID: a.ID, OwnerID: a.CreatedBy})
		if newWork(&updated, nil).status(task).Done {
			telemetry.Emit(ctx, telemetry.Event{Name: "task_completed", EntityID: issueID, Key: task.Key, ClassID: a.ClassID, AssignmentID: a.ID, OwnerID: a.CreatedBy})
		}
	}
	event := AnswerEvent{
		ID: idgen.New(), OrgID: actor.OrgID, AttemptID: in.AttemptID,
		StudentID: actor.StudentID, ExerciseID: in.ExerciseID, Step: in.Step,
		Answer: truncate(in.Answer, 4000), IsCorrect: check.Correct, ScoreDelta: check.Score,
		Kind: check.Kind,
	}
	go s.recordEventAsync(context.WithoutCancel(ctx), event)

	return AnswerResult{Attempt: updated, Correct: check.Correct, Expected: check.Expected}, nil
}

// saveAnswer записывает ответ, прогресс по заданию и, если это было
// последнее несделанное задание урока, завершает попытку — одной
// транзакцией.
//
// Одной, потому что это один факт: «ученик ответил». Ответ без отметки
// задания оставил бы учителю клетку «—» при отвеченном вопросе, а отметка
// без ответа — «сделано» без ответа. Запись ответа блокирует строку попытки
// до конца транзакции, и последний ответ с последним «Дальше» не проходят
// мимо друг друга.
func (s *Service) saveAnswer(ctx context.Context, orgID uuid.UUID, in AnswerInput, check AnswerCheck,
	key string, given GivenAnswer, task LessonTask, issueID uuid.UUID, tasks []LessonTask) (Attempt, error) {
	var updated Attempt
	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.requireOpenIssue(ctx, tx, orgID, issueID); err != nil {
			return err
		}
		var err error
		updated, err = s.store.AdvanceAttempt(ctx, tx, orgID, in.AttemptID,
			in.Step, check.Score, check.Correct, key, given)
		if err != nil {
			return err
		}
		if task.Key == "" {
			return nil
		}
		// Последний вопрос упражнения отмечает задание сделанным: от
		// открытия до этого момента считаются минуты на задание.
		done := newWork(&updated, nil).status(task).Done
		if err := s.store.MarkTaskProgress(ctx, tx, orgID, updated.ID, task.Key, issueID, done); err != nil {
			return err
		}
		completed, err := s.completeIfDone(ctx, tx, orgID, updated, tasks)
		if err != nil {
			return err
		}
		if completed {
			now := time.Now()
			updated.FinishedAt, updated.Completed = &now, true
		}
		return nil
	})
	return updated, err
}

// recordEventAsync пишет событие в стороне от ответа ученику.
func (s *Service) recordEventAsync(ctx context.Context, e AnswerEvent) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.store.InsertAnswerEvent(ctx, s.pool, e); err != nil {
		// Потерянное событие — это дыра в статистике, а не сломанный урок.
		// Поэтому лог, а не ошибка наверх.
		logx.From(ctx).Error("не удалось записать событие ответа",
			"attempt_id", e.AttemptID, "err", err)
	}
}

// FinishLesson отмечает прохождение завершённым.
//
// С выдачей по заданиям попытка завершается сама, когда сделаны все задания
// урока. Ручка осталась ради совместимости и завершает попытку только в
// этом случае; иначе возвращает её как есть. Завершать досрочно нельзя:
// завершённая попытка ответов не принимает, и ученик, нажавший «Завершить»
// после урока, не смог бы сделать домашнюю работу по тому же уроку.
func (s *Service) FinishLesson(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (Attempt, error) {
	attempt, err := s.store.AttemptByID(ctx, s.pool, actor.OrgID, attemptID)
	if errors.Is(err, ErrNotFound) {
		return Attempt{}, httpx.NotFound("Попытка не найдена")
	}
	if err != nil {
		return Attempt{}, httpx.Internal("Не удалось завершить урок").Wrap(err)
	}
	if attempt.StudentID != actor.StudentID {
		return Attempt{}, httpx.NotFound("Попытка не найдена")
	}
	_, b, err := s.attemptBoard(ctx, actor, attempt)
	if err != nil {
		return Attempt{}, err
	}
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		locked, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attempt.ID)
		if err != nil {
			return err
		}
		_, err = s.completeIfDone(ctx, tx, actor.OrgID, locked, b.tasks)
		return err
	})
	if err != nil {
		return Attempt{}, httpx.Internal("Не удалось завершить урок").Wrap(err)
	}
	attempt, err = s.store.AttemptByID(ctx, s.pool, actor.OrgID, attemptID)
	if err != nil {
		return Attempt{}, httpx.Internal("Не удалось завершить урок").Wrap(err)
	}
	return attempt, nil
}

// Progress — прогресс класса по назначению, для учителя.
func (s *Service) Progress(ctx context.Context, actor iam.Actor, assignmentID uuid.UUID) ([]Attempt, error) {
	if _, err := s.Assignment(ctx, actor, assignmentID); err != nil {
		return nil, err
	}
	out, err := s.store.AttemptsOfAssignment(ctx, s.pool, actor.OrgID, assignmentID)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	}
	return out, nil
}

// studentAssignment достаёт назначение и проверяет, что ученик в этом классе.
func (s *Service) studentAssignment(ctx context.Context, actor iam.Actor, id uuid.UUID) (Assignment, error) {
	a, _, err := s.studentAssignmentClass(ctx, actor, id)
	return a, err
}

// studentAssignmentClass — то же, что studentAssignment, и вдобавок класс
// назначения: его название стоит в «Результатах урока».
func (s *Service) studentAssignmentClass(ctx context.Context, actor iam.Actor, id uuid.UUID) (Assignment, ClassInfo, error) {
	a, err := s.store.AssignmentByID(ctx, s.pool, actor.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		return Assignment{}, ClassInfo{}, httpx.NotFound("Задание не найдено")
	}
	if err != nil {
		return Assignment{}, ClassInfo{}, httpx.Internal("Не удалось прочитать задание").Wrap(err)
	}

	classes, err := s.classes.StudentClasses(ctx, actor)
	if err != nil {
		return Assignment{}, ClassInfo{}, err
	}
	for _, c := range classes {
		if c.ID == a.ClassID {
			return a, c, nil
		}
	}
	// Ученик не в этом классе — 404, а не 403: существование чужого
	// задания его не касается.
	return Assignment{}, ClassInfo{}, httpx.NotFound("Задание не найдено")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
